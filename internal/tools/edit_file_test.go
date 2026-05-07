package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFile_NormalReplace(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(f, []byte("hello world\nfoo bar\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &EditFileTool{WorkDir: dir}
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  f,
		"old_string": "foo",
		"new_string": "baz",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world\nbaz bar\n" {
		t.Errorf("file content = %q, want %q", got, "hello world\nbaz bar\n")
	}
	if !strings.Contains(result, "File updated") {
		t.Errorf("result = %q, want containing 'File updated'", result)
	}
}

func TestEditFile_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(f, []byte("aaa\nbbb\naaa\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &EditFileTool{WorkDir: dir}
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path":   f,
		"old_string":  "aaa",
		"new_string":  "ccc",
		"replace_all": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ccc\nbbb\nccc\n" {
		t.Errorf("file content = %q, want %q", got, "ccc\nbbb\nccc\n")
	}
	if !strings.Contains(result, "2 replacements") {
		t.Errorf("result = %q, want containing '2 replacements'", result)
	}
}

func TestEditFile_UniquenessError(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(f, []byte("aaa\nbbb\naaa\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &EditFileTool{WorkDir: dir}
	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  f,
		"old_string": "aaa",
		"new_string": "ccc",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "matches 2 locations") {
		t.Errorf("error = %q, want containing 'matches 2 locations'", err.Error())
	}
}

func TestEditFile_OldStringNotFound(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(f, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &EditFileTool{WorkDir: dir}
	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  f,
		"old_string": "xyz",
		"new_string": "abc",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "old_string not found in file") {
		t.Errorf("error = %q, want containing 'old_string not found in file'", err.Error())
	}
}

func TestEditFile_EmptyOldString(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(f, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &EditFileTool{WorkDir: dir}
	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  f,
		"old_string": "",
		"new_string": "abc",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "old_string must not be empty") {
		t.Errorf("error = %q, want containing 'old_string must not be empty'", err.Error())
	}
}

func TestEditFile_DiffPreview(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(f, []byte("hello world\nfoo bar\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &EditFileTool{WorkDir: dir}
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  f,
		"old_string": "foo",
		"new_string": "baz",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "-") || !strings.Contains(result, "+") {
		t.Errorf("result = %q, want diff markers (+ and -)", result)
	}
}

func TestEditFile_SandboxEscape(t *testing.T) {
	dir := t.TempDir()

	tool := &EditFileTool{WorkDir: dir}
	_, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  "../../../etc/passwd",
		"old_string": "root",
		"new_string": "hacked",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "SANDBOX_ESCAPE") {
		t.Errorf("error = %q, want containing 'SANDBOX_ESCAPE'", err.Error())
	}
}
