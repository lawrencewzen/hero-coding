package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// GrepTool searches file contents by wrapping ripgrep (rg).
type GrepTool struct {
	WorkDir      string
	GetEscalated func() []string
}

func (t *GrepTool) Definition() ToolDef {
	return ToolDef{
		Type: "function",
		Function: FunctionDef{
			Name:        "grep",
			Description: `Search file contents for a regex pattern using ripgrep. Returns matching file paths by default; use output_mode "content" to see matching lines with context.`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Search pattern (regular expression).",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "File or directory to search (default: current working directory).",
					},
					"glob": map[string]any{
						"type":        "string",
						"description": "Filter files by glob pattern (e.g. '*.go', '*.{ts,tsx}'), maps to rg --glob.",
					},
					"ignore_case": map[string]any{
						"type":        "boolean",
						"description": "Case-insensitive search (default: false).",
					},
					"context": map[string]any{
						"type":        "integer",
						"description": "Number of lines to show before and after each match, maps to rg -C.",
					},
					"before": map[string]any{
						"type":        "integer",
						"description": "Number of lines to show before each match, maps to rg -B.",
					},
					"after": map[string]any{
						"type":        "integer",
						"description": "Number of lines to show after each match, maps to rg -A.",
					},
					"output_mode": map[string]any{
						"type":        "string",
						"description": `Output mode: "content" shows matching lines, "files_with_matches" shows file paths, "count" shows match counts. Default: "files_with_matches".`,
						"enum":        []string{"content", "files_with_matches", "count"},
					},
					"multiline": map[string]any{
						"type":        "boolean",
						"description": "Enable multiline mode where . matches newlines and patterns can span lines (rg -U --multiline-dotall). Default: false.",
					},
					"type": map[string]any{
						"type":        "string",
						"description": "File type to search (e.g. 'go', 'js', 'py'), maps to rg --type.",
					},
					"head_limit": map[string]any{
						"type":        "integer",
						"description": "Limit output to first N lines (default: 250).",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "Skip first N lines before applying head_limit (default: 0).",
					},
				},
				"required": []string{"pattern"},
			},
		},
	}
}

func (t *GrepTool) Execute(ctx context.Context, args map[string]any) (result string, err error) {
	defer recoverExecute(&err)

	pattern, err := getRequiredStringArg(args, "pattern")
	if err != nil {
		return "", err
	}

	// Determine search path.
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

	// Parse optional parameters.
	outputMode := "files_with_matches"
	if v, ok := args["output_mode"].(string); ok && v != "" {
		outputMode = v
	}

	ignoreCase := false
	if v, ok := args["ignore_case"].(bool); ok {
		ignoreCase = v
	}

	multiline := false
	if v, ok := args["multiline"].(bool); ok {
		multiline = v
	}

	contextLines := -1
	if v, ok := args["context"]; ok {
		if n, ok := toInt(v); ok && n >= 0 {
			contextLines = n
		}
	}

	beforeLines := -1
	if v, ok := args["before"]; ok {
		if n, ok := toInt(v); ok && n >= 0 {
			beforeLines = n
		}
	}

	afterLines := -1
	if v, ok := args["after"]; ok {
		if n, ok := toInt(v); ok && n >= 0 {
			afterLines = n
		}
	}

	headLimit := 250
	if v, ok := args["head_limit"]; ok {
		if n, ok := toInt(v); ok && n >= 0 {
			headLimit = n
		}
	}

	offset := 0
	if v, ok := args["offset"]; ok {
		if n, ok := toInt(v); ok && n >= 0 {
			offset = n
		}
	}

	glob := ""
	if v, ok := args["glob"].(string); ok {
		glob = v
	}

	fileType := ""
	if v, ok := args["type"].(string); ok {
		fileType = v
	}

	// Build rg args.
	rgArgs := []string{"--color", "never"}

	switch outputMode {
	case "files_with_matches":
		rgArgs = append(rgArgs, "--files-with-matches")
	case "count":
		rgArgs = append(rgArgs, "--count")
	case "content":
		rgArgs = append(rgArgs, "--no-heading", "--line-number")
	}

	if ignoreCase {
		rgArgs = append(rgArgs, "-i")
	}

	if contextLines >= 0 {
		rgArgs = append(rgArgs, "-C", strconv.Itoa(contextLines))
	}
	if beforeLines >= 0 {
		rgArgs = append(rgArgs, "-B", strconv.Itoa(beforeLines))
	}
	if afterLines >= 0 {
		rgArgs = append(rgArgs, "-A", strconv.Itoa(afterLines))
	}

	if multiline {
		rgArgs = append(rgArgs, "-U", "--multiline-dotall")
	}

	if fileType != "" {
		rgArgs = append(rgArgs, "--type", fileType)
	}

	if glob != "" {
		rgArgs = append(rgArgs, "--glob", glob)
	}

	// Positional args: pattern, then search path.
	// Use -- to prevent patterns like "--help" from being parsed as flags.
	rgArgs = append(rgArgs, "--", pattern, searchPath)

	// Execute rg. Set cmd.Dir to a valid directory (searchPath may be a file).
	cmd := exec.CommandContext(ctx, "rg", rgArgs...)
	if info, err := os.Stat(searchPath); err == nil && info.IsDir() {
		cmd.Dir = searchPath
	} else {
		cmd.Dir = filepath.Dir(searchPath)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if rg is not installed.
		if execErr, ok := err.(*exec.Error); ok && execErr.Err == exec.ErrNotFound {
			return "", fmt.Errorf("ripgrep (rg) is not installed")
		}

		// rg exit code 1 = no matches, exit code 2 = error.
		if exitErr, ok := err.(*exec.ExitError); ok {
			switch exitErr.ExitCode() {
			case 1:
				return "No matches found.", nil
			case 2:
				return "", fmt.Errorf("rg error: %s", strings.TrimSpace(string(output)))
			}
		}

		return "", fmt.Errorf("rg error: %w", err)
	}

	// Post-process: apply offset and head_limit.
	out := strings.TrimRight(string(output), "\n")
	if out == "" {
		return "No matches found.", nil
	}

	lines := strings.Split(out, "\n")

	// Apply offset.
	if offset > 0 {
		if offset >= len(lines) {
			return "No matches found.", nil
		}
		lines = lines[offset:]
	}

	// Apply head_limit.
	if headLimit > 0 && headLimit < len(lines) {
		lines = lines[:headLimit]
	}

	return strings.Join(lines, "\n"), nil
}

