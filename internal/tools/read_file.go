package tools

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	readMaxLines    = 2000
	readMaxImageMB  = 20
	readLargeFileMB = 100
)

// ReadFileTool reads a file with optional offset/limit, truncating at 2000 lines.
type ReadFileTool struct {
	WorkDir      string
	ReadTracker  *ReadTracker
	GetEscalated func() []string
}

func (t *ReadFileTool) Definition() ToolDef {
	return ToolDef{
		Type: "function",
		Function: FunctionDef{
			Name:        "read_file",
			Description: "Read the contents of a file with line numbers. Supports partial reads via offset and limit (truncates at 2000 lines). Can also read images (base64) and PDFs (via pdftotext).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file to read (relative or absolute).",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "Line number to start reading from (1-indexed). Defaults to 1.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of lines to read.",
					},
					"pages": map[string]any{
						"type":        "string",
						"description": "Page range for PDF files (e.g. \"1-5\", \"3\", \"10-20\"). Defaults to 1-20. Max 20 pages per request.",
					},
				},
				"required": []string{"path"},
			},
		},
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, args map[string]any) (result string, err error) {
	defer recoverExecute(&err)

	path, err := getRequiredStringArg(args, "path")
	if err != nil {
		return "", err
	}
	escalated := []string{}
	if t.GetEscalated != nil {
		escalated = t.GetEscalated()
	}
	absPath, err := sandboxPathEx(t.WorkDir, escalated, path)
	if err != nil {
		return "", err
	}

	// Stat the file.
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a file", absPath)
	}
	fileSize := info.Size()

	// Detect content type from first 512 bytes.
	mime, err := detectMIME(absPath)
	if err != nil {
		return "", err
	}

	// Route by content type.
	var out string
	switch {
	case strings.HasPrefix(mime, "image/"):
		out, err = t.readImage(absPath, mime, fileSize)
	case mime == "application/pdf":
		out, err = t.readPDF(ctx, absPath, args, fileSize)
	case isTextMIME(mime):
		out, err = t.readText(absPath, args, fileSize)
	default:
		return "", fmt.Errorf("Binary file (type: %s, size: %s). Use bash with file, hexdump, or strings to inspect.", mime, formatSize(fileSize))
	}
	if err != nil {
		return "", err
	}

	// Track the read only after successful completion.
	if t.ReadTracker != nil {
		t.ReadTracker.Record(absPath)
	}

	return out, nil
}

// readImage base64-encodes an image file and returns it with the ImageBase64Prefix.
func (t *ReadFileTool) readImage(absPath, mime string, fileSize int64) (string, error) {
	if fileSize > readMaxImageMB*1024*1024 {
		return "", fmt.Errorf("image too large (%s, max %dMB)", formatSize(fileSize), readMaxImageMB)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return ImageBase64Prefix + "data:" + mime + ";base64," + encoded, nil
}

// readPDF uses pdftotext to extract text from a PDF file.
func (t *ReadFileTool) readPDF(ctx context.Context, absPath string, args map[string]any, fileSize int64) (string, error) {
	first, last := 1, 20
	if pagesStr, ok := args["pages"]; ok {
		if s, ok := pagesStr.(string); ok && s != "" {
			f, l, err := parsePageRange(s)
			if err != nil {
				return "", err
			}
			first, last = f, l
		}
	}
	if last-first+1 > 20 {
		return "", fmt.Errorf("max 20 pages per request (requested %d-%d = %d pages)", first, last, last-first+1)
	}

	pdftotextPath, err := exec.LookPath("pdftotext")
	if err != nil {
		return "", fmt.Errorf("pdftotext not installed. Install poppler-utils (apt install poppler-utils / brew install poppler) to read PDFs.")
	}

	cmd := exec.CommandContext(ctx, pdftotextPath,
		"-f", strconv.Itoa(first),
		"-l", strconv.Itoa(last),
		absPath, "-",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pdftotext failed: %w", err)
	}

	header := fmt.Sprintf("[File: %s | %s | Pages %d-%d]\n",
		filepath.Base(absPath), formatSize(fileSize), first, last)
	return header + string(out), nil
}

// readText reads a text file with line numbers and metadata header.
// Total line count is computed in a single pass alongside the content read.
func (t *ReadFileTool) readText(absPath string, args map[string]any, fileSize int64) (string, error) {
	offset := 1
	if v, ok := args["offset"]; ok {
		if n, ok := toInt(v); ok && n > 0 {
			offset = n
		}
	}

	limit := readMaxLines
	if v, ok := args["limit"]; ok {
		if n, ok := toInt(v); ok && n > 0 {
			limit = n
		}
	}

	f, err := os.Open(absPath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	// Skip lines before offset.
	skipped := 0
	for skipped < offset-1 {
		if !scanner.Scan() {
			return "", fmt.Errorf("offset %d is beyond end of file (%d lines)", offset, skipped)
		}
		skipped++
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	// Collect up to limit lines with line numbers.
	var sb strings.Builder
	linesRead := 0
	lineNum := offset
	for linesRead < limit && scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(&sb, "%6d\t%s\n", lineNum, line)
		lineNum++
		linesRead++
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	if linesRead == 0 {
		return "", fmt.Errorf("offset %d is beyond end of file", offset)
	}

	startLine := offset
	endLine := offset + linesRead - 1

	// Count remaining lines to get total (single pass, no second read).
	// For very large files (>readLargeFileMB), skip counting to avoid slow scans.
	hasMore := false
	totalLines := -1
	if fileSize <= readLargeFileMB*1024*1024 {
		remaining := 0
		for scanner.Scan() {
			remaining++
		}
		hasMore = remaining > 0
		totalLines = skipped + linesRead + remaining
	} else {
		hasMore = scanner.Scan()
	}

	header := buildHeader(absPath, totalLines, fileSize, startLine, endLine, hasMore)
	return header + sb.String(), nil
}

// buildHeader constructs the file metadata header line.
func buildHeader(absPath string, totalLines int, fileSize int64, startLine, endLine int, hasMore bool) string {
	name := filepath.Base(absPath)
	sizeStr := formatSize(fileSize)

	var parts []string
	parts = append(parts, "File: "+name)
	if totalLines >= 0 {
		parts = append(parts, fmt.Sprintf("%d lines", totalLines))
	}
	parts = append(parts, sizeStr)
	parts = append(parts, fmt.Sprintf("Showing lines %d-%d", startLine, endLine))

	header := "[" + strings.Join(parts, " | ") + "]\n"

	if hasMore {
		header += fmt.Sprintf("Use offset=%d to continue reading.\n", endLine+1)
	}
	return header
}

// formatSize formats a file size in human-readable form.
func formatSize(size int64) string {
	if size >= 1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	}
	return fmt.Sprintf("%.1f KB", float64(size)/1024)
}

// detectMIME reads the first 512 bytes of a file and returns its MIME type.
func detectMIME(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for detection: %w", err)
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read file for detection: %w", err)
	}
	return http.DetectContentType(buf[:n]), nil
}

// isTextMIME returns true if the MIME type represents text content.
func isTextMIME(mime string) bool {
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	textAppTypes := []string{
		"application/json",
		"application/xml",
		"application/javascript",
		"application/x-sh",
		"application/x-httpd-php",
		"application/x-perl",
		"application/x-python",
		"application/x-ruby",
		"application/x-yaml",
		"application/toml",
		"application/x-ndjson",
		"application/graphql",
		"application/sql",
		"application/xhtml+xml",
		"application/ld+json",
	}
	for _, t := range textAppTypes {
		if mime == t {
			return true
		}
	}
	// Match patterns like application/*+xml, application/*+json
	if strings.HasPrefix(mime, "application/") {
		if strings.HasSuffix(mime, "+xml") || strings.HasSuffix(mime, "+json") {
			return true
		}
	}
	return false
}

// parsePageRange parses a page range string like "1-5", "3", or "10-20".
func parsePageRange(s string) (first, last int, err error) {
	s = strings.TrimSpace(s)
	if parts := strings.SplitN(s, "-", 2); len(parts) == 2 {
		first, err = strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return 0, 0, fmt.Errorf("invalid page range %q: %w", s, err)
		}
		last, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, 0, fmt.Errorf("invalid page range %q: %w", s, err)
		}
	} else {
		first, err = strconv.Atoi(s)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid page number %q: %w", s, err)
		}
		last = first
	}
	if first < 1 || last < first {
		return 0, 0, fmt.Errorf("invalid page range: %d-%d", first, last)
	}
	return first, last, nil
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}
