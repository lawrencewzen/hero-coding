package verifier

import (
	"context"
	"strings"
	"testing"
	"time"

	"hero-coding/internal/story"
)

// makeStory wraps a flat list of commands as a single "default" tier so
// existing tests keep their original semantics.
func makeStory(t *testing.T, verify []string) *story.Story {
	t.Helper()
	var tiers []story.VerifyTier
	if len(verify) > 0 {
		tiers = []story.VerifyTier{{Name: "default", Commands: verify}}
	}
	return &story.Story{
		Filepath: "/tmp/us.md",
		Frontmatter: story.Frontmatter{
			ID: "us-test", Title: "Test", Priority: story.PriorityNormal,
			Verify: tiers,
		},
	}
}

// makeTieredStory builds a story with explicit ordered tiers — used by the
// fail-fast and silent-on-success tests.
func makeTieredStory(t *testing.T, tiers ...story.VerifyTier) *story.Story {
	t.Helper()
	return &story.Story{
		Filepath: "/tmp/us.md",
		Frontmatter: story.Frontmatter{
			ID: "us-test", Title: "Test", Priority: story.PriorityNormal,
			Verify: tiers,
		},
	}
}

func TestRun_SkipWhenNoCommands(t *testing.T) {
	r, err := Run(context.Background(), Options{
		Story: makeStory(t, nil), TargetRepo: t.TempDir(), Round: 1,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Skipped || !r.AllPassed || len(r.Commands) != 0 {
		t.Fatalf("expected skipped & passed & empty, got %+v", r)
	}
}

func TestRun_AllGreen(t *testing.T) {
	r, err := Run(context.Background(), Options{
		Story: makeStory(t, []string{"true", "echo ok"}), TargetRepo: t.TempDir(),
		Round: 1, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Skipped || !r.AllPassed || len(r.Commands) != 2 {
		t.Fatalf("expected !skipped & passed & 2 cmds, got %+v", r)
	}
	if r.Commands[0].ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", r.Commands[0].ExitCode)
	}
}

func TestRun_ContinueAfterFailure(t *testing.T) {
	r, err := Run(context.Background(), Options{
		Story: makeStory(t, []string{"false", "echo afterfail"}), TargetRepo: t.TempDir(),
		Round: 1, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.AllPassed {
		t.Fatal("expected fail")
	}
	if r.Commands[0].ExitCode == 0 {
		t.Fatal("expected first cmd non-zero")
	}
	if r.Commands[1].ExitCode != 0 {
		t.Fatalf("expected second cmd 0, got %d", r.Commands[1].ExitCode)
	}
	if !strings.Contains(r.Commands[1].StdoutTail, "afterfail") {
		t.Fatalf("expected stdout to contain afterfail, got %q", r.Commands[1].StdoutTail)
	}
}

func TestRun_FallbackToDefaults(t *testing.T) {
	r, err := Run(context.Background(), Options{
		Story: makeStory(t, nil), TargetRepo: t.TempDir(),
		Round: 1, Timeout: 5 * time.Second,
		DefaultCommands: []string{"echo from-default"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Skipped || len(r.Commands) != 1 {
		t.Fatalf("expected !skipped & 1 cmd, got %+v", r)
	}
	if !strings.Contains(r.Commands[0].StdoutTail, "from-default") {
		t.Fatalf("stdout missing default echo, got %q", r.Commands[0].StdoutTail)
	}
}

func TestRun_Timeout(t *testing.T) {
	r, err := Run(context.Background(), Options{
		Story: makeStory(t, []string{"sleep 5"}), TargetRepo: t.TempDir(),
		Round: 1, Timeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.AllPassed {
		t.Fatal("expected fail on timeout")
	}
	if !r.Commands[0].TimedOut || r.Commands[0].ExitCode != 124 {
		t.Fatalf("expected timeout (exit 124), got %+v", r.Commands[0])
	}
}

func TestSummarize_FailureReport(t *testing.T) {
	r, err := Run(context.Background(), Options{
		Story: makeStory(t, []string{"sh -c 'echo nope >&2; exit 7'"}),
		TargetRepo: t.TempDir(), Round: 1, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := Summarize(r)
	if !strings.Contains(s, "FAIL") || !strings.Contains(s, "exit=7") {
		t.Fatalf("expected FAIL + exit=7 in summary, got %q", s)
	}
}

// Tiered fail-fast: tier 1 passes, tier 2 fails (but its remaining cmds
// still run, per "tier 内全跑完"), tier 3 is wholly skipped.
func TestRun_Tiered_FailFastBetweenTiers(t *testing.T) {
	r, err := Run(context.Background(), Options{
		Story: makeTieredStory(t,
			story.VerifyTier{Name: "typecheck", Commands: []string{"true"}},
			story.VerifyTier{Name: "lint", Commands: []string{"false", "echo lint-after-fail"}},
			story.VerifyTier{Name: "unit", Commands: []string{"echo should-not-run"}},
		),
		TargetRepo: t.TempDir(), Round: 1, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.AllPassed {
		t.Fatal("expected fail")
	}

	// 4 records: typecheck.true, lint.false, lint.echo, unit.echo (latter is skipped marker)
	if len(r.Commands) != 4 {
		t.Fatalf("expected 4 records, got %d (%+v)", len(r.Commands), r.Commands)
	}
	// Typecheck ran and passed.
	if r.Commands[0].Tier != "typecheck" || r.Commands[0].Skipped || r.Commands[0].ExitCode != 0 {
		t.Fatalf("typecheck record wrong: %+v", r.Commands[0])
	}
	// Lint's first cmd ran and failed.
	if r.Commands[1].Tier != "lint" || r.Commands[1].Skipped || r.Commands[1].ExitCode == 0 {
		t.Fatalf("lint[0] should be failed, got %+v", r.Commands[1])
	}
	// Lint's second cmd still ran (run-to-completion within tier).
	if r.Commands[2].Tier != "lint" || r.Commands[2].Skipped || r.Commands[2].ExitCode != 0 {
		t.Fatalf("lint[1] should have run + passed, got %+v", r.Commands[2])
	}
	// Unit got skipped wholesale.
	if r.Commands[3].Tier != "unit" || !r.Commands[3].Skipped {
		t.Fatalf("unit[0] should be skipped, got %+v", r.Commands[3])
	}

	// Summary should mention the lint tier and the skipped unit tier.
	s := Summarize(r)
	if !strings.Contains(s, `"lint"`) {
		t.Fatalf("summary should mention failing tier 'lint': %s", s)
	}
	if !strings.Contains(s, "unit") {
		t.Fatalf("summary should mention skipped tier 'unit': %s", s)
	}
}

// silent-on-success: all-passing record produces a one-line summary, no
// per-command output dumps.
func TestSummarize_SilentOnSuccess(t *testing.T) {
	r, err := Run(context.Background(), Options{
		Story: makeTieredStory(t,
			story.VerifyTier{Name: "typecheck", Commands: []string{"true"}},
			story.VerifyTier{Name: "unit", Commands: []string{"echo ok"}},
		),
		TargetRepo: t.TempDir(), Round: 1, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.AllPassed {
		t.Fatalf("expected all passed, got %+v", r)
	}
	s := Summarize(r)
	if strings.Contains(s, "echo ok") || strings.Contains(s, "stdout") {
		t.Fatalf("summary should not include command stdout on success: %s", s)
	}
	if !strings.Contains(s, "verifier OK") {
		t.Fatalf("expected 'verifier OK' header, got %s", s)
	}
}
