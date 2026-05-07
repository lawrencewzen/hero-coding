package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileTool creates or overwrites a file with the given content.
type WriteFileTool struct {
	WorkDir      string
	ReadTracker  *ReadTracker
	GetEscalated func() []string
}

func (t *WriteFileTool) Definition() ToolDef {
	return ToolDef{
		Type: "function",
		Function: FunctionDef{
			Name: "write_file",
			Description: "Create a new file or overwrite an existing file with the provided content. Overwriting requires a prior read_file call on the same path.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path": map[string]any{
						"type":        "string",
						"description": "Absolute or relative path for the file to write.",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "The complete file content to write.",
					},
				},
				"required": []string{"file_path", "content"},
			},
		},
	}
}

func (t *WriteFileTool) Execute(ctx context.Context, args map[string]any) (result string, err error) {
	defer recoverExecute(&err)

	rawPath, err := getRequiredStringArg(args, "file_path")
	if err != nil {
		return "", err
	}

	content, err := getRequiredStringArg(args, "content")
	if err != nil {
		return "", err
	}

	escalated := []string{}
	if t.GetEscalated != nil {
		escalated = t.GetEscalated()
	}
	absPath, err := sandboxPathEx(t.WorkDir, escalated, rawPath)
	if err != nil {
		return "", err
	}

	// Read old content for diff preview (also enforces read-before-write).
	var oldContent string
	isNew := true
	if _, statErr := os.Stat(absPath); statErr == nil {
		isNew = false
		if t.ReadTracker != nil && !t.ReadTracker.HasRead(absPath) {
			return "", fmt.Errorf("file already exists: %s. Read it with read_file first before overwriting", absPath)
		}
		old, readErr := os.ReadFile(absPath)
		if readErr == nil {
			oldContent = string(old)
		}
	}

	// Create parent directories if needed.
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return "", fmt.Errorf("create directories: %w", err)
	}

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	header := fmt.Sprintf("File written: %s (%d bytes)\n", absPath, len(content))
	if isNew {
		return header + "(new file)", nil
	}

	diff := simpleDiff(oldContent, content, 2, 40)
	return header + diff, nil
}
