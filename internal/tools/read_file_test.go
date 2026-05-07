package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFile_Normal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	var lines []string
	for i := 1; i <= 5; i++ {
		lines = append(lines, fmt.Sprintf("line %d content", i))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadFileTool{WorkDir: dir}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "hello.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Header should contain filename.
	if !strings.Contains(result, "hello.txt") {
		t.Error("result should contain filename in header")
	}
	// Should contain all 5 lines with line numbers.
	for i := 1; i <= 5; i++ {
		lineContent := fmt.Sprintf("line %d content", i)
		if !strings.Contains(result, lineContent) {
			t.Errorf("result should contain %q", lineContent)
		}
		lineNum := fmt.Sprintf("%6d\t", i)
		if !strings.Contains(result, lineNum) {
			t.Errorf("result should contain line number %d", i)
		}
	}
}

func TestReadFile_OffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ten.txt")
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadFileTool{WorkDir: dir}
	result, err := tool.Execute(context.Background(), map[string]any{
		"path":   "ten.txt",
		"offset": 3,
		"limit":  3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Showing lines 3-5") {
		t.Errorf("header should show lines 3-5, got:\n%s", result)
	}
	// Lines 3, 4, 5 should be present.
	for i := 3; i <= 5; i++ {
		if !strings.Contains(result, fmt.Sprintf("line %d", i)) {
			t.Errorf("result should contain line %d", i)
		}
	}
	// Lines 1, 2 should NOT appear as content (they may appear in header metadata).
	// Check that the numbered output does not include them.
	if strings.Contains(result, fmt.Sprintf("%6d\tline 1", 1)) {
		t.Error("result should not contain line 1 output")
	}
	if strings.Contains(result, fmt.Sprintf("%6d\tline 2", 2)) {
		t.Error("result should not contain line 2 output")
	}
}

func TestReadFile_OffsetBeyondFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadFileTool{WorkDir: dir}
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":   "short.txt",
		"offset": 10,
	})
	if err == nil {
		t.Fatal("expected error for offset beyond file")
	}
	if !strings.Contains(err.Error(), "beyond end of file") {
		t.Errorf("error should mention 'beyond end of file', got: %v", err)
	}
}

func TestReadFile_BinaryFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.dat")
	data := make([]byte, 512)
	// Fill with null bytes to make it clearly binary.
	for i := range data {
		data[i] = 0x00
	}
	data[0] = 0x01
	data[1] = 0x02
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadFileTool{WorkDir: dir}
	_, err := tool.Execute(context.Background(), map[string]any{"path": "binary.dat"})
	if err == nil {
		t.Fatal("expected error for binary file")
	}
	if !strings.Contains(err.Error(), "Binary file") {
		t.Errorf("error should mention 'Binary file', got: %v", err)
	}
}

func TestReadFile_ImageFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.png")
	// Valid PNG header signature.
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	data := append(pngHeader, make([]byte, 100)...)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadFileTool{WorkDir: dir}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "test.png"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result, ImageBase64Prefix) {
		t.Errorf("result should start with ImageBase64Prefix %q, got prefix: %q",
			ImageBase64Prefix, result[:min(len(result), 30)])
	}
}

func TestReadFile_ReadTrackerRecorded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tracker := NewReadTracker()
	tool := &ReadFileTool{WorkDir: dir, ReadTracker: tracker}

	absPath := filepath.Join(dir, "tracked.txt")
	if tracker.HasRead(absPath) {
		t.Fatal("file should not be tracked before read")
	}

	_, err := tool.Execute(context.Background(), map[string]any{"path": "tracked.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !tracker.HasRead(absPath) {
		t.Error("file should be tracked after successful read")
	}
}

func TestReadFile_SandboxEscape(t *testing.T) {
	dir := t.TempDir()
	tool := &ReadFileTool{WorkDir: dir}

	_, err := tool.Execute(context.Background(), map[string]any{
		"path": "../../../etc/passwd",
	})
	if err == nil {
		t.Fatal("expected error for sandbox escape")
	}
	if !strings.Contains(err.Error(), "SANDBOX_ESCAPE") {
		t.Errorf("error should mention 'SANDBOX_ESCAPE', got: %v", err)
	}
}

func TestReadFile_Directory(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	tool := &ReadFileTool{WorkDir: dir}
	_, err := tool.Execute(context.Background(), map[string]any{"path": "subdir"})
	if err == nil {
		t.Fatal("expected error for directory path")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("error should mention 'is a directory', got: %v", err)
	}
}
