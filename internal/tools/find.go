package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

const findDefaultLimit = 1000
const findMaxOutputBytes = 50 * 1024

type fileEntry struct {
	path    string
	modTime time.Time
}

// FindTool searches for files matching a glob pattern.
type FindTool struct {
	WorkDir      string
	GetEscalated func() []string
}

func (t *FindTool) Definition() ToolDef {
	return ToolDef{
		Type: "function",
		Function: FunctionDef{
			Name:        "find",
			Description: "Find files matching a glob pattern. Results are sorted by modification time (newest first). Skips .git, node_modules, and vendor directories.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Glob pattern to match files, e.g. '*.go', '**/*.json', 'src/**/*.ts'.",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to search in (default: current working directory).",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results (default: 1000).",
					},
				},
				"required": []string{"pattern"},
			},
		},
	}
}

func (t *FindTool) Execute(ctx context.Context, args map[string]any) (result string, err error) {
	defer recoverExecute(&err)

	pattern, err := getRequiredStringArg(args, "pattern")
	if err != nil {
		return "", err
	}

	searchPath := defaultWorkDir(t.WorkDir)
	if v, ok := args["path"].(string); ok && v != "" {
		escalated := []string{}
		if t.GetEscalated != nil {
			escalated = t.GetEscalated()
		}
		p, err := sandboxPathEx(t.WorkDir, escalated, v)
		if err != nil {
			return "", err
		}
		searchPath = p
	}

	limit := findDefaultLimit
	if v, ok := args["limit"]; ok {
		if n, ok := toInt(v); ok && n > 0 {
			limit = n
		}
	}

	if _, err := os.Stat(searchPath); err != nil {
		return "", fmt.Errorf("path not found: %s", searchPath)
	}

	// Hard ceiling to prevent OOM on huge repos. We collect all matches up to
	// this cap, sort by mtime, then return the top `limit`.
	const maxCollect = 100_000

	var entries []fileEntry
	capReached := false

	err = filepath.WalkDir(searchPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		if len(entries) >= maxCollect {
			capReached = true
			return filepath.SkipAll
		}

		// Match pattern against the relative path (POSIX separators).
		rel, err := filepath.Rel(searchPath, path)
		if err != nil {
			return nil
		}
		relPosix := filepath.ToSlash(rel)

		matched, err := doublestar.Match(pattern, relPosix)
		if err != nil || !matched {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		entries = append(entries, fileEntry{
			path:    relPosix,
			modTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk error: %w", err)
	}

	if len(entries) == 0 {
		return "No files found matching pattern.", nil
	}

	// Sort by modification time descending (most recently modified first).
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime.After(entries[j].modTime)
	})

	// Truncate to requested limit after sorting.
	truncated := len(entries) > limit
	if truncated {
		entries = entries[:limit]
	}

	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.path
	}

	out := strings.Join(paths, "\n")
	if len(out) > findMaxOutputBytes {
		out = out[:findMaxOutputBytes] + "\n[Output truncated at 50 KB]"
	}

	if truncated || capReached {
		out += fmt.Sprintf("\n[Showing %d of matching files, sorted by modification time. Refine the pattern for more targeted results.]", limit)
	}

	return out, nil
}

