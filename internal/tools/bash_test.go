package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashTool_NormalCommand(t *testing.T) {
	bt := &BashTool{}
	result, err := bt.Execute(context.Background(), map[string]any{
		"command": "echo hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("expected output to contain 'hello', got %q", result)
	}
}

func TestBashTool_WorkDir(t *testing.T) {
	dir := t.TempDir()
	filename := "testfile_workdir.txt"
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	bt := &BashTool{WorkDir: dir}
	result, err := bt.Execute(context.Background(), map[string]any{
		"command": "ls",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, filename) {
		t.Errorf("expected output to contain %q, got %q", filename, result)
	}
}

func TestBashTool_StderrMerged(t *testing.T) {
	bt := &BashTool{}
	result, err := bt.Execute(context.Background(), map[string]any{
		"command": "echo out && echo err >&2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "out") {
		t.Errorf("expected output to contain 'out', got %q", result)
	}
	if !strings.Contains(result, "err") {
		t.Errorf("expected output to contain 'err', got %q", result)
	}
}

func TestBashTool_Timeout(t *testing.T) {
	bt := &BashTool{}
	start := time.Now()
	_, err := bt.Execute(context.Background(), map[string]any{
		"command": "sleep 30",
		"timeout": 1000,
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected error containing 'timed out', got %q", err.Error())
	}
	if elapsed > 10*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}

func TestBashTool_ContextCancel(t *testing.T) {
	bt := &BashTool{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := bt.Execute(ctx, map[string]any{
		"command": "sleep 30",
	})
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "cancelled") && !strings.Contains(errMsg, "canceled") {
		t.Errorf("expected error containing 'cancelled' or 'canceled', got %q", errMsg)
	}
}

func TestBashTool_NonZeroExit(t *testing.T) {
	bt := &BashTool{}
	_, err := bt.Execute(context.Background(), map[string]any{
		"command": "exit 1",
	})
	if err == nil {
		t.Fatal("expected non-nil error for exit 1")
	}
}

func TestBashTool_MissingCommand(t *testing.T) {
	bt := &BashTool{}
	_, err := bt.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing command")
	}
	if !strings.Contains(err.Error(), "command is required") {
		t.Errorf("expected 'command is required' error, got %q", err.Error())
	}
}

func TestBashTool_OutputTruncation(t *testing.T) {
	bt := &BashTool{}
	// Generate 10000 'x' characters using printf
	result, err := bt.Execute(context.Background(), map[string]any{
		"command": `printf 'x%.0s' $(seq 1 10000)`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	runeCount := len([]rune(result))
	if runeCount > maxToolOutputChars {
		t.Errorf("expected output <= %d runes, got %d", maxToolOutputChars, runeCount)
	}
}
