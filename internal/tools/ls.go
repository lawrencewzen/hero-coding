package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
)

const lsDefaultLimit = 500

// LsTool lists the contents of a directory.
type LsTool struct {
	WorkDir      string
	GetEscalated func() []string
}

func (t *LsTool) Definition() ToolDef {
	return ToolDef{
		Type: "function",
		Function: FunctionDef{
			Name:        "ls",
			Description: "List the contents of a directory. Directories are shown with a trailing /.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to list (default: current working directory).",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of entries to return (default: 500).",
					},
				},
				"required": []string{},
			},
		},
	}
}

func (t *LsTool) Execute(ctx context.Context, args map[string]any) (result string, err error) {
	defer recoverExecute(&err)

	dirPath := defaultWorkDir(t.WorkDir)
	if v, ok := args["path"].(string); ok && v != "" {
		escalated := []string{}
		if t.GetEscalated != nil {
			escalated = t.GetEscalated()
		}
		p, err := sandboxPathEx(t.WorkDir, escalated, v)
		if err != nil {
			return "", err
		}
		dirPath = p
	}

	limit := lsDefaultLimit
	if v, ok := args["limit"]; ok {
		if n, ok := toInt(v); ok && n > 0 {
			limit = n
		}
	}

	info, err := os.Stat(dirPath)
	if err != nil {
		return "", fmt.Errorf("path not found: %s", dirPath)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", dirPath)
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return "", fmt.Errorf("cannot read directory: %w", err)
	}

	// Sort case-insensitively.
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})

	var lines []string
	limitReached := false

	for _, entry := range entries {
		if len(lines) >= limit {
			limitReached = true
			break
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}

	if len(lines) == 0 {
		return "(empty directory)", nil
	}

	out := strings.Join(lines, "\n")
	const maxOutputBytes = 50 * 1024
	if len(out) > maxOutputBytes {
		out = out[:maxOutputBytes] + "\n[Output truncated at 50 KB]"
	}

	if limitReached {
		out += fmt.Sprintf("\n[%d entries limit reached. Use limit=%d for more.]", limit, limit*2)
	}

	return out, nil
}

