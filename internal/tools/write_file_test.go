package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFile_NewFile(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteFileTool{
		WorkDir:     dir,
		ReadTracker: NewReadTracker(),
	}

	target := filepath.Join(dir, "hello.txt")
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": target,
		"content":   "hello world",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("content mismatch: got %q", string(data))
	}
	if !strings.Contains(result, "(new file)") {
		t.Errorf("result should contain '(new file)', got: %s", result)
	}
}

func TestWriteFile_AutoCreateDirs(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteFileTool{
		WorkDir:     dir,
		ReadTracker: NewReadTracker(),
	}

	target := filepath.Join(dir, "subdir", "deep", "file.txt")
	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path": target,
		"content":   "nested content",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestWriteFile_OverwriteAfterRead(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(target, []byte("old content"), 0644); err != nil {
		t.Fatal(err)
	}

	tracker := NewReadTracker()
	tracker.Record(target)

	tool := &WriteFileTool{
		WorkDir:     dir,
		ReadTracker: tracker,
	}

	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": target,
		"content":   "new content",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new content" {
		t.Errorf("content not updated: got %q", string(data))
	}
	if strings.Contains(result, "(new file)") {
		t.Errorf("overwrite should not contain '(new file)', got: %s", result)
	}
}

func TestWriteFile_RejectOverwriteUnread(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(target, []byte("old content"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &WriteFileTool{
		WorkDir:     dir,
		ReadTracker: NewReadTracker(),
	}

	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path": target,
		"content":   "new content",
	})
	if err == nil {
		t.Fatal("expected error for unread file overwrite")
	}
	if !strings.Contains(err.Error(), "Read it with read_file first") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestWriteFile_DiffPreview(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "diff.txt")
	if err := os.WriteFile(target, []byte("line1\nline2\nline3\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tracker := NewReadTracker()
	tracker.Record(target)

	tool := &WriteFileTool{
		WorkDir:     dir,
		ReadTracker: tracker,
	}

	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": target,
		"content":   "line1\nmodified\nline3\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Diff output should contain diff markers (- for removed, + for added).
	if !strings.Contains(result, "-") || !strings.Contains(result, "+") {
		t.Errorf("expected diff markers in result, got: %s", result)
	}
	if strings.Contains(result, "(new file)") {
		t.Errorf("should not contain '(new file)' for overwrite, got: %s", result)
	}
}

func TestWriteFile_SandboxEscape(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteFileTool{
		WorkDir:     dir,
		ReadTracker: NewReadTracker(),
	}

	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path": "../../../tmp/evil",
		"content":   "malicious",
	})
	if err == nil {
		t.Fatal("expected error for sandbox escape")
	}
	if !strings.Contains(err.Error(), "SANDBOX_ESCAPE") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestWriteFile_NilReadTracker(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(target, []byte("old content"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &WriteFileTool{
		WorkDir:     dir,
		ReadTracker: nil,
	}

	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path": target,
		"content":   "overwritten without read",
	})
	if err != nil {
		t.Fatalf("nil ReadTracker should allow overwrite, got error: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "overwritten without read" {
		t.Errorf("content not updated: got %q", string(data))
	}
}
