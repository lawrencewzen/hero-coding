package verifier

import (
	"context"
	"strings"
	"testing"
	"time"

	"hero-coding/internal/story"
)

func makeStory(t *testing.T, verify []string) *story.Story {
	t.Helper()
	return &story.Story{
		Filepath: "/tmp/us.md",
		Frontmatter: story.Frontmatter{
			ID: "us-test", Title: "Test", Priority: story.PriorityNormal,
			Verify: verify,
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
