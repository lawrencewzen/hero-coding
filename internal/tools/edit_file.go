package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

type EditFileTool struct {
	WorkDir      string
	GetEscalated func() []string
}

func (t *EditFileTool) Definition() ToolDef {
	return ToolDef{
		Type: "function",
		Function: FunctionDef{
			Name: "edit_file",
			Description: "Replace a string in a file. old_string must be unique in the file (otherwise the call fails); set replace_all to true to replace all occurrences.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_path": map[string]any{
						"type":        "string",
						"description": "The file path to edit.",
					},
					"old_string": map[string]any{
						"type":        "string",
						"description": "The string to replace.",
					},
					"new_string": map[string]any{
						"type":        "string",
						"description": "The replacement string.",
					},
					"replace_all": map[string]any{
						"type":        "boolean",
						"description": "Replace all occurrences of old_string. Default false.",
					},
				},
				"required": []string{"file_path", "old_string", "new_string"},
			},
		},
	}
}

func (t *EditFileTool) Execute(ctx context.Context, args map[string]any) (result string, err error) {
	defer recoverExecute(&err)

	rawPath, err := getRequiredStringArg(args, "file_path")
	if err != nil {
		return "", err
	}
	escalated := []string{}
	if t.GetEscalated != nil {
		escalated = t.GetEscalated()
	}
	filePath, err := sandboxPathEx(t.WorkDir, escalated, rawPath)
	if err != nil {
		return "", err
	}

	oldString, err := getRequiredStringArg(args, "old_string")
	if err != nil {
		return "", err
	}
	if oldString == "" {
		return "", errors.New("old_string must not be empty")
	}

	newString, err := getRequiredStringArg(args, "new_string")
	if err != nil {
		return "", err
	}

	replaceAll := false
	if v, ok := args["replace_all"]; ok {
		if b, ok := v.(bool); ok {
			replaceAll = b
		}
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	source := string(content)

	count := strings.Count(source, oldString)
	if count == 0 {
		return "", errors.New("old_string not found in file")
	}
	if count > 1 && !replaceAll {
		return "", fmt.Errorf(
			"old_string matches %d locations in the file. Include more surrounding context lines to make old_string unique, or set replace_all=true to replace all occurrences.",
			count,
		)
	}

	info, err := os.Stat(filePath)
	if err != nil {
		return "", err
	}

	var updated string
	var header string

	if replaceAll {
		updated = strings.ReplaceAll(source, oldString, newString)
		lines := findAllMatchLines(source, oldString)
		header = fmt.Sprintf("File updated: %d replacements at lines %s\n", count, formatLineNumbers(lines))
	} else {
		updated = strings.Replace(source, oldString, newString, 1)
		pos := strings.Index(source, oldString)
		lineNum := strings.Count(source[:pos], "\n") + 1
		header = fmt.Sprintf("File updated: line %d\n", lineNum)
	}

	if err := os.WriteFile(filePath, []byte(updated), info.Mode()); err != nil {
		return "", err
	}

	diff := simpleDiff(source, updated, 2, 40)
	return header + diff, nil
}

// findAllMatchLines returns 1-based line numbers for every occurrence of sub in s.
func findAllMatchLines(s, sub string) []int {
	var lines []int
	offset := 0
	for {
		idx := strings.Index(s[offset:], sub)
		if idx < 0 {
			break
		}
		pos := offset + idx
		lineNum := strings.Count(s[:pos], "\n") + 1
		lines = append(lines, lineNum)
		offset = pos + len(sub)
	}
	return lines
}

// formatLineNumbers formats a slice of ints as a comma-separated string.
func formatLineNumbers(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("%d", n)
	}
	return strings.Join(parts, ", ")
}

