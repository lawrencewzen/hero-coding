package tools

import (
	"hero-coding/internal/tooldef"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxToolOutputChars = 8000

// ImageBase64Prefix is the sentinel prefix returned by Tool.Execute when
// the result is a base64-encoded image. The agent loop detects this prefix
// and converts the tool result into a multimodal ChatMessage with an
// image_url content block instead of plain text.
const ImageBase64Prefix = "__IMAGE_BASE64:"

// Type aliases so all existing tool files in this package continue to work
// without modification.
type Tool = tooldef.Tool
type ToolDef = tooldef.ToolDef
type FunctionDef = tooldef.FunctionDef

func recoverExecute(err *error) {
	if r := recover(); r != nil {
		switch v := r.(type) {
		case error:
			*err = fmt.Errorf("tool panicked: %w", v)
		default:
			*err = fmt.Errorf("tool panicked: %v", v)
		}
	}
}

func getRequiredStringArg(args map[string]any, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}

	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}

	return str, nil
}

// sandboxPath resolves path relative to workDir, then verifies the result
// stays within workDir (including symlink resolution). Returns an error if
// the resolved path escapes the sandbox. If workDir is empty, any path is allowed.
func sandboxPath(workDir, path string) (string, error) {
	// No sandbox enforcement when workDir is unset.
	if workDir == "" {
		return filepath.Clean(path), nil
	}

	// Resolve workDir symlinks once so the boundary is real.
	cleanWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}

	// Build the lexical resolved path (relative to the real workDir).
	var resolved string
	if filepath.IsAbs(path) {
		resolved = filepath.Clean(path)
	} else {
		resolved = filepath.Clean(filepath.Join(cleanWorkDir, path))
	}

	// Resolve symlinks on the target path (partial — leaf may not exist yet).
	// This handles both ../.. traversal AND symlink escapes in a single check,
	// and avoids false positives from alias paths (e.g. /var vs /private/var on macOS).
	real, err := evalSymlinksPartial(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	if real != cleanWorkDir && !strings.HasPrefix(real, cleanWorkDir+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace %q", path, workDir)
	}

	return resolved, nil
}

// sandboxPathEx is like sandboxPath but also accepts a list of user-approved
// escalated paths. If the path escapes workDir but falls under an escalated path,
// it is allowed. Returns an error when access is denied.
func sandboxPathEx(workDir string, escalated []string, path string) (string, error) {
	resolved, err := sandboxPath(workDir, path)
	if err == nil {
		return resolved, nil
	}
	// Check escalated paths.
	// Resolve the target path lexically for comparison.
	var target string
	if filepath.IsAbs(path) {
		target = filepath.Clean(path)
	} else if workDir != "" {
		target = filepath.Clean(filepath.Join(workDir, path))
	} else {
		target = filepath.Clean(path)
	}
	real, _ := evalSymlinksPartial(target)
	for _, ep := range escalated {
		cleanEP := filepath.Clean(ep)
		if real == cleanEP || strings.HasPrefix(real, cleanEP+string(filepath.Separator)) {
			return target, nil
		}
	}
	return "", fmt.Errorf("SANDBOX_ESCAPE: path %q is outside your workspace — call request_permission to request access", path)
}

// evalSymlinksPartial resolves symlinks for the longest existing prefix of p.
// For paths where the leaf doesn't exist yet (e.g. write_file creating a new file),
// it walks up to the nearest existing ancestor and resolves that.
func evalSymlinksPartial(p string) (string, error) {
	if _, err := os.Lstat(p); err == nil {
		return filepath.EvalSymlinks(p)
	}
	parent := filepath.Dir(p)
	if parent == p {
		// Reached filesystem root.
		return p, nil
	}
	resolvedParent, err := evalSymlinksPartial(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(p)), nil
}

// defaultWorkDir returns workDir if non-empty, otherwise falls back to os.Getwd().
func defaultWorkDir(workDir string) string {
	if workDir != "" {
		return workDir
	}
	wd, _ := os.Getwd()
	return wd
}

func truncateString(s string, max int) string {
	if max <= 0 {
		return ""
	}

	runes := []rune(s)
	if len(runes) <= max {
		return s
	}

	return string(runes[:max])
}

// simpleDiff generates a unified-style diff between old and new content using
// a Myers-like O(ND) algorithm. Shows changed hunks with surrounding context
// lines. Output is capped at maxDiffLines to avoid flooding the LLM context.
func simpleDiff(oldContent, newContent string, contextLines, maxDiffLines int) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	ops := diffOps(oldLines, newLines)

	var lines []editOp
	n := len(ops)

	// Find changed regions and add context around them.
	i := 0
	for i < n {
		// Skip unchanged lines.
		if ops[i].kind == ' ' {
			i++
			continue
		}

		// Found a change — collect context before.
		start := i - contextLines
		if start < 0 {
			start = 0
		}
		for j := start; j < i; j++ {
			lines = append(lines, ops[j])
		}

		// Collect the changed lines and any interleaved context.
		for i < n {
			lines = append(lines, ops[i])
			if ops[i].kind == ' ' {
				// Check if there's another change within context distance.
				allContext := true
				end := i + 1 + 2*contextLines
				if end > n {
					end = n
				}
				for k := i + 1; k < end; k++ {
					if ops[k].kind != ' ' {
						allContext = false
						break
					}
				}
				if allContext {
					// No nearby change — emit trailing context and break.
					trailing := 0
					i++
					for i < n && ops[i].kind == ' ' && trailing < contextLines {
						lines = append(lines, ops[i])
						trailing++
						i++
					}
					break
				}
			}
			i++
		}
	}

	if len(lines) == 0 {
		return "(no changes)"
	}

	if len(lines) > maxDiffLines {
		lines = lines[:maxDiffLines]
	}

	var sb strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&sb, "%c | %s\n", l.kind, l.text)
	}
	if len(lines) == maxDiffLines {
		sb.WriteString("  ... [diff truncated]\n")
	}

	return sb.String()
}

type editOp struct {
	kind byte
	text string
}

// diffOps computes an edit script (sequence of ' ', '-', '+' operations)
// between old and new line slices using the LCS (longest common subsequence).
func diffOps(oldLines, newLines []string) []editOp {
	m, n := len(oldLines), len(newLines)

	// For very large diffs, fall back to a simple prefix/suffix approach to avoid O(mn) memory.
	if int64(m)*int64(n) > 10_000_000 {
		return diffOpsFallback(oldLines, newLines)
	}

	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	// Backtrack to produce edit ops.
	var ops []editOp
	i, j := 0, 0
	for i < m && j < n {
		if oldLines[i] == newLines[j] {
			ops = append(ops, editOp{kind: ' ', text: oldLines[i]})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			ops = append(ops, editOp{kind: '-', text: oldLines[i]})
			i++
		} else {
			ops = append(ops, editOp{kind: '+', text: newLines[j]})
			j++
		}
	}
	for ; i < m; i++ {
		ops = append(ops, editOp{kind: '-', text: oldLines[i]})
	}
	for ; j < n; j++ {
		ops = append(ops, editOp{kind: '+', text: newLines[j]})
	}
	return ops
}

// diffOpsFallback handles very large files where O(mn) is too expensive.
// Uses a simpler matching from both ends approach.
func diffOpsFallback(oldLines, newLines []string) []editOp {
	var ops []editOp

	// Match common prefix.
	i, j := 0, 0
	for i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j] {
		ops = append(ops, editOp{kind: ' ', text: oldLines[i]})
		i++
		j++
	}

	// Match common suffix.
	oi, nj := len(oldLines)-1, len(newLines)-1
	var suffix []editOp
	for oi >= i && nj >= j && oldLines[oi] == newLines[nj] {
		suffix = append(suffix, editOp{kind: ' ', text: oldLines[oi]})
		oi--
		nj--
	}

	// Everything in between is a change.
	for ; i <= oi; i++ {
		ops = append(ops, editOp{kind: '-', text: oldLines[i]})
	}
	for ; j <= nj; j++ {
		ops = append(ops, editOp{kind: '+', text: newLines[j]})
	}

	// Append suffix in reverse.
	for k := len(suffix) - 1; k >= 0; k-- {
		ops = append(ops, suffix[k])
	}

	return ops
}
