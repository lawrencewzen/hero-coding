package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// truncateString
// ---------------------------------------------------------------------------

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"short string", "hello", 10, "hello"},
		{"equal to max", "hello", 5, "hello"},
		{"exceeds max", "hello world", 5, "hello"},
		{"max zero", "hello", 0, ""},
		{"max negative", "hello", -1, ""},
		{"empty string", "", 5, ""},
		{"multibyte utf8 within limit", "你好世界", 4, "你好世界"},
		{"multibyte utf8 truncated", "你好世界", 2, "你好"},
		{"multibyte utf8 max one", "你好世界", 1, "你"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateString(tt.s, tt.max)
			if got != tt.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// getRequiredStringArg
// ---------------------------------------------------------------------------

func TestGetRequiredStringArg(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]any
		key     string
		want    string
		wantErr string
	}{
		{
			name: "key exists as string",
			args: map[string]any{"name": "alice"},
			key:  "name",
			want: "alice",
		},
		{
			name:    "key missing",
			args:    map[string]any{},
			key:     "name",
			wantErr: "name is required",
		},
		{
			name:    "key is int",
			args:    map[string]any{"name": 42},
			key:     "name",
			wantErr: "name must be a string",
		},
		{
			name: "key is empty string",
			args: map[string]any{"name": ""},
			key:  "name",
			want: "",
		},
		{
			name:    "key is bool",
			args:    map[string]any{"flag": true},
			key:     "flag",
			wantErr: "flag must be a string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getRequiredStringArg(tt.args, tt.key)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// sandboxPath
// ---------------------------------------------------------------------------

func TestSandboxPath(t *testing.T) {
	t.Run("empty workDir allows any path", func(t *testing.T) {
		got, err := sandboxPath("", "/etc/passwd")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "/etc/passwd" {
			t.Errorf("got %q, want %q", got, "/etc/passwd")
		}
	})

	t.Run("relative path within workDir", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}

		got, err := sandboxPath(dir, "sub/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// The resolved path should be within workDir.
		realDir, _ := filepath.EvalSymlinks(dir)
		want := filepath.Join(realDir, "sub", "file.txt")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("absolute path within workDir", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}

		realDir, _ := filepath.EvalSymlinks(dir)
		absPath := filepath.Join(realDir, "sub", "file.txt")
		got, err := sandboxPath(dir, absPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != absPath {
			t.Errorf("got %q, want %q", got, absPath)
		}
	})

	t.Run("escape via dotdot", func(t *testing.T) {
		dir := t.TempDir()
		_, err := sandboxPath(dir, "../../../etc/passwd")
		if err == nil {
			t.Fatal("expected error for path escape, got nil")
		}
		if !strings.Contains(err.Error(), "escapes workspace") {
			t.Errorf("error = %q, want containing 'escapes workspace'", err.Error())
		}
	})

	t.Run("symlink escape", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "escape_link")
		if err := os.Symlink("/tmp", link); err != nil {
			t.Skip("cannot create symlink:", err)
		}

		_, err := sandboxPath(dir, "escape_link/somefile")
		if err == nil {
			t.Fatal("expected error for symlink escape, got nil")
		}
		if !strings.Contains(err.Error(), "escapes workspace") {
			t.Errorf("error = %q, want containing 'escapes workspace'", err.Error())
		}
	})

	t.Run("non-existent leaf within workDir", func(t *testing.T) {
		dir := t.TempDir()
		got, err := sandboxPath(dir, "newfile.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		realDir, _ := filepath.EvalSymlinks(dir)
		want := filepath.Join(realDir, "newfile.txt")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// evalSymlinksPartial
// ---------------------------------------------------------------------------

func TestEvalSymlinksPartial(t *testing.T) {
	t.Run("existing file resolves fully", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "existing.txt")
		if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil {
			t.Fatal(err)
		}

		got, err := evalSymlinksPartial(f)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want, _ := filepath.EvalSymlinks(f)
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("non-existent leaf resolves parent", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "newfile.txt")

		got, err := evalSymlinksPartial(p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		realDir, _ := filepath.EvalSymlinks(dir)
		want := filepath.Join(realDir, "newfile.txt")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("non-existent multi-level walks up", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "a", "b", "c.txt")

		got, err := evalSymlinksPartial(p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		realDir, _ := filepath.EvalSymlinks(dir)
		want := filepath.Join(realDir, "a", "b", "c.txt")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("symlink in parent chain is resolved", func(t *testing.T) {
		dir := t.TempDir()
		realSub := filepath.Join(dir, "real")
		if err := os.MkdirAll(realSub, 0o755); err != nil {
			t.Fatal(err)
		}
		linkSub := filepath.Join(dir, "link")
		if err := os.Symlink(realSub, linkSub); err != nil {
			t.Skip("cannot create symlink:", err)
		}

		p := filepath.Join(linkSub, "newfile.txt")
		got, err := evalSymlinksPartial(p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		realDir, _ := filepath.EvalSymlinks(dir)
		want := filepath.Join(realDir, "real", "newfile.txt")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// simpleDiff
// ---------------------------------------------------------------------------

func TestSimpleDiff(t *testing.T) {
	t.Run("same content", func(t *testing.T) {
		got := simpleDiff("hello\nworld", "hello\nworld", 3, 100)
		if got != "(no changes)" {
			t.Errorf("got %q, want %q", got, "(no changes)")
		}
	})

	t.Run("single line replacement", func(t *testing.T) {
		got := simpleDiff("aaa\nbbb\nccc", "aaa\nBBB\nccc", 1, 100)
		if !strings.Contains(got, "- | bbb") {
			t.Errorf("expected deleted line 'bbb' in diff:\n%s", got)
		}
		if !strings.Contains(got, "+ | BBB") {
			t.Errorf("expected added line 'BBB' in diff:\n%s", got)
		}
	})

	t.Run("multi-line insertion", func(t *testing.T) {
		got := simpleDiff("a\nb", "a\nx\ny\nb", 1, 100)
		if !strings.Contains(got, "+ | x") || !strings.Contains(got, "+ | y") {
			t.Errorf("expected inserted lines in diff:\n%s", got)
		}
	})

	t.Run("multi-line deletion", func(t *testing.T) {
		got := simpleDiff("a\nx\ny\nb", "a\nb", 1, 100)
		if !strings.Contains(got, "- | x") || !strings.Contains(got, "- | y") {
			t.Errorf("expected deleted lines in diff:\n%s", got)
		}
	})

	t.Run("mixed add and delete", func(t *testing.T) {
		got := simpleDiff("a\nb\nc", "a\nB\nC", 0, 100)
		if !strings.Contains(got, "- | b") || !strings.Contains(got, "+ | B") {
			t.Errorf("expected changed lines in diff:\n%s", got)
		}
	})

	t.Run("maxDiffLines truncation", func(t *testing.T) {
		got := simpleDiff("a\nb\nc\nd\ne", "A\nB\nC\nD\nE", 1, 3)
		if !strings.Contains(got, "... [diff truncated]") {
			t.Errorf("expected truncation marker in diff:\n%s", got)
		}
	})

	t.Run("empty old all additions", func(t *testing.T) {
		got := simpleDiff("", "a\nb", 1, 100)
		if !strings.Contains(got, "+ | a") || !strings.Contains(got, "+ | b") {
			t.Errorf("expected all additions in diff:\n%s", got)
		}
	})

	t.Run("empty new all deletions", func(t *testing.T) {
		got := simpleDiff("a\nb", "", 1, 100)
		if !strings.Contains(got, "- | a") || !strings.Contains(got, "- | b") {
			t.Errorf("expected all deletions in diff:\n%s", got)
		}
	})
}

// ---------------------------------------------------------------------------
// diffOps
// ---------------------------------------------------------------------------

func TestDiffOps(t *testing.T) {
	t.Run("both empty", func(t *testing.T) {
		ops := diffOps([]string{}, []string{})
		if len(ops) != 0 {
			t.Errorf("expected 0 ops, got %d", len(ops))
		}
	})

	t.Run("completely same", func(t *testing.T) {
		ops := diffOps([]string{"a", "b", "c"}, []string{"a", "b", "c"})
		for _, op := range ops {
			if op.kind != ' ' {
				t.Errorf("expected all unchanged, got kind=%c text=%q", op.kind, op.text)
			}
		}
		if len(ops) != 3 {
			t.Errorf("expected 3 ops, got %d", len(ops))
		}
	})

	t.Run("completely different", func(t *testing.T) {
		ops := diffOps([]string{"a", "b"}, []string{"x", "y"})
		deletes := 0
		adds := 0
		for _, op := range ops {
			switch op.kind {
			case '-':
				deletes++
			case '+':
				adds++
			}
		}
		if deletes != 2 || adds != 2 {
			t.Errorf("expected 2 deletes and 2 adds, got %d deletes %d adds", deletes, adds)
		}
	})

	t.Run("head difference", func(t *testing.T) {
		ops := diffOps([]string{"x", "b", "c"}, []string{"a", "b", "c"})
		if ops[0].kind != '-' || ops[0].text != "x" {
			t.Errorf("expected first op to delete 'x', got kind=%c text=%q", ops[0].kind, ops[0].text)
		}
	})

	t.Run("middle difference", func(t *testing.T) {
		ops := diffOps([]string{"a", "x", "c"}, []string{"a", "y", "c"})
		found := false
		for _, op := range ops {
			if op.kind == '+' && op.text == "y" {
				found = true
			}
		}
		if !found {
			t.Error("expected '+y' in ops")
		}
	})

	t.Run("tail difference", func(t *testing.T) {
		ops := diffOps([]string{"a", "b", "x"}, []string{"a", "b", "y"})
		last := ops[len(ops)-1]
		if last.kind != '+' || last.text != "y" {
			t.Errorf("expected last op to add 'y', got kind=%c text=%q", last.kind, last.text)
		}
	})
}

// ---------------------------------------------------------------------------
// diffOpsFallback
// ---------------------------------------------------------------------------

func TestDiffOpsFallback(t *testing.T) {
	t.Run("only prefix same", func(t *testing.T) {
		ops := diffOpsFallback([]string{"a", "b", "c"}, []string{"a", "b", "X"})
		// prefix: a, b are same; c -> X
		if ops[0].kind != ' ' || ops[0].text != "a" {
			t.Errorf("first op should be unchanged 'a', got kind=%c text=%q", ops[0].kind, ops[0].text)
		}
		if ops[1].kind != ' ' || ops[1].text != "b" {
			t.Errorf("second op should be unchanged 'b', got kind=%c text=%q", ops[1].kind, ops[1].text)
		}
		hasDel, hasAdd := false, false
		for _, op := range ops {
			if op.kind == '-' && op.text == "c" {
				hasDel = true
			}
			if op.kind == '+' && op.text == "X" {
				hasAdd = true
			}
		}
		if !hasDel || !hasAdd {
			t.Errorf("expected delete 'c' and add 'X', ops=%v", ops)
		}
	})

	t.Run("only suffix same", func(t *testing.T) {
		ops := diffOpsFallback([]string{"c", "b", "a"}, []string{"X", "b", "a"})
		hasDel, hasAdd := false, false
		for _, op := range ops {
			if op.kind == '-' && op.text == "c" {
				hasDel = true
			}
			if op.kind == '+' && op.text == "X" {
				hasAdd = true
			}
		}
		if !hasDel || !hasAdd {
			t.Errorf("expected delete 'c' and add 'X', ops=%v", ops)
		}
		// suffix b, a should be unchanged
		last := ops[len(ops)-1]
		secondLast := ops[len(ops)-2]
		if last.kind != ' ' || last.text != "a" {
			t.Errorf("last should be unchanged 'a', got kind=%c text=%q", last.kind, last.text)
		}
		if secondLast.kind != ' ' || secondLast.text != "b" {
			t.Errorf("second-to-last should be unchanged 'b', got kind=%c text=%q", secondLast.kind, secondLast.text)
		}
	})

	t.Run("both prefix and suffix same", func(t *testing.T) {
		ops := diffOpsFallback(
			[]string{"a", "b", "OLD", "c", "d"},
			[]string{"a", "b", "NEW", "c", "d"},
		)
		// a, b prefix; c, d suffix; OLD -> NEW in middle
		if ops[0].kind != ' ' || ops[0].text != "a" {
			t.Errorf("expected prefix 'a'")
		}
		hasDel, hasAdd := false, false
		for _, op := range ops {
			if op.kind == '-' && op.text == "OLD" {
				hasDel = true
			}
			if op.kind == '+' && op.text == "NEW" {
				hasAdd = true
			}
		}
		if !hasDel || !hasAdd {
			t.Errorf("expected delete 'OLD' and add 'NEW'")
		}
		last := ops[len(ops)-1]
		if last.kind != ' ' || last.text != "d" {
			t.Errorf("expected suffix 'd' at end")
		}
	})

	t.Run("completely different", func(t *testing.T) {
		ops := diffOpsFallback([]string{"a", "b"}, []string{"x", "y"})
		deletes, adds := 0, 0
		for _, op := range ops {
			switch op.kind {
			case '-':
				deletes++
			case '+':
				adds++
			}
		}
		if deletes != 2 || adds != 2 {
			t.Errorf("expected 2 deletes and 2 adds, got %d deletes %d adds", deletes, adds)
		}
	})
}
