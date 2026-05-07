package story

import (
	"os"
	"path/filepath"
	"testing"
)

func writeStory(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParse_VerifyAndScope(t *testing.T) {
	p := writeStory(t, "us-x.md", `---
id: us-x
title: Demo
verify:
  - npm test
  - npm run typecheck
scope:
  - src/**
  - tests/**
---

body
`)
	s, err := Parse(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := s.Frontmatter.Verify; len(got) != 2 || got[0] != "npm test" || got[1] != "npm run typecheck" {
		t.Fatalf("verify mismatch: %#v", got)
	}
	if got := s.Frontmatter.Scope; len(got) != 2 || got[0] != "src/**" {
		t.Fatalf("scope mismatch: %#v", got)
	}
}

func TestParse_OptionalFields(t *testing.T) {
	p := writeStory(t, "us-y.md", `---
id: us-y
title: NoExtras
---

body`)
	s, err := Parse(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Frontmatter.Verify != nil || s.Frontmatter.Scope != nil {
		t.Fatalf("expected nil verify+scope, got %#v / %#v", s.Frontmatter.Verify, s.Frontmatter.Scope)
	}
}

func TestParse_RejectsEmptyVerifyEntry(t *testing.T) {
	p := writeStory(t, "us-z.md", `---
id: us-z
title: BadVerify
verify:
  - ""
---

body`)
	if _, err := Parse(p); err == nil {
		t.Fatal("expected error for empty verify entry")
	}
}

func TestParse_RejectsUnsafeID(t *testing.T) {
	p := writeStory(t, "bad.md", `---
id: "../oops"
title: T
---

body`)
	if _, err := Parse(p); err == nil {
		t.Fatal("expected error for unsafe id")
	}
}
