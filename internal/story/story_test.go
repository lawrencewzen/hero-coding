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

func TestParse_FlatVerifyBecomesDefaultTier(t *testing.T) {
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
	tiers := s.Frontmatter.Verify
	if len(tiers) != 1 {
		t.Fatalf("expected 1 default tier, got %d (%#v)", len(tiers), tiers)
	}
	if tiers[0].Name != "default" {
		t.Fatalf("expected tier name 'default', got %q", tiers[0].Name)
	}
	if got := tiers[0].Commands; len(got) != 2 || got[0] != "npm test" || got[1] != "npm run typecheck" {
		t.Fatalf("commands mismatch: %#v", got)
	}
	if got := s.Frontmatter.Scope; len(got) != 2 || got[0] != "src/**" {
		t.Fatalf("scope mismatch: %#v", got)
	}
}

func TestParse_TieredVerifyPreservesOrder(t *testing.T) {
	// YAML mapping nodes preserve declaration order; we rely on this for
	// fail-fast semantics, so verify it round-trips correctly.
	p := writeStory(t, "us-tier.md", `---
id: us-tier
title: Tiered
verify:
  typecheck:
    - go build ./...
  lint:
    - go vet ./...
    - golangci-lint run
  unit:
    - go test ./...
---

body
`)
	s, err := Parse(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tiers := s.Frontmatter.Verify
	if len(tiers) != 3 {
		t.Fatalf("expected 3 tiers, got %d (%#v)", len(tiers), tiers)
	}
	wantNames := []string{"typecheck", "lint", "unit"}
	for i, w := range wantNames {
		if tiers[i].Name != w {
			t.Fatalf("tier %d: want %q, got %q", i, w, tiers[i].Name)
		}
	}
	if tiers[1].Commands[0] != "go vet ./..." || tiers[1].Commands[1] != "golangci-lint run" {
		t.Fatalf("lint tier commands mismatch: %#v", tiers[1].Commands)
	}
}

func TestParse_RejectsEmptyTier(t *testing.T) {
	p := writeStory(t, "us-empty-tier.md", `---
id: us-empty-tier
title: Bad
verify:
  typecheck: []
---
body`)
	if _, err := Parse(p); err == nil {
		t.Fatal("expected error for empty tier")
	}
}

func TestParse_RejectsScalarVerify(t *testing.T) {
	p := writeStory(t, "us-scalar.md", `---
id: us-scalar
title: Bad
verify: "go test"
---
body`)
	if _, err := Parse(p); err == nil {
		t.Fatal("expected error for scalar verify (must be list or map)")
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
