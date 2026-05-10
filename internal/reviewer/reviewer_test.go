package reviewer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"hero-coding/internal/config"
	"hero-coding/internal/role"
	"hero-coding/internal/state"
	"hero-coding/internal/story"
)

var defaultReviewerRole = role.Defaults().Reviewer

func makeStory() *story.Story {
	return &story.Story{
		Filepath:    "/tmp/us.md",
		Frontmatter: story.Frontmatter{ID: "us-x", Title: "T", Priority: story.PriorityNormal},
		Body:        "story body",
	}
}

func TestRun_ShortCircuitOnVerifierFail(t *testing.T) {
	v := state.VerifierRecord{
		Round: 1, Skipped: false, AllPassed: false, WallMs: 10,
		Commands: []state.VerifierCommandRecord{{
			Cmd: "npm test", ExitCode: 1, DurationMs: 5,
			StderrTail: "AssertionError",
		}},
	}
	// LLM is never called — give it a URL that would fail if hit.
	rv, err := Run(context.Background(), Options{
		Story:      makeStory(),
		Reviewer:   config.LLMConfig{BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"},
		Role:       defaultReviewerRole,
		TargetRepo: "/tmp",
		BaseRef:    "main",
		Round:      1,
		Verifier:   v,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rv.Verdict != state.VerdictChangesRequested {
		t.Errorf("verdict: want CHANGES_REQUESTED, got %q", rv.Verdict)
	}
	if !rv.ShortCircuited {
		t.Errorf("expected ShortCircuited=true on verifier-fail short-circuit")
	}
}

// initRepo makes `dir` a git repo with a single empty commit on main, so
// reviewer.collectGitContext has something to diff against.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestRun_ParsesApprovedReview(t *testing.T) {
	repo := initRepo(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"verdict\":\"APPROVED\",\"summary\":\"looks good\",\"ac_check\":[],\"comments\":[]}"}}]}`)
	}))
	defer srv.Close()

	rv, err := Run(context.Background(), Options{
		Story:      makeStory(),
		Reviewer:   config.LLMConfig{BaseURL: srv.URL, APIKey: "k", Model: "m"},
		Role:       defaultReviewerRole,
		TargetRepo: repo,
		BaseRef:    "main",
		Round:      1,
		Verifier:   state.VerifierRecord{Round: 1, Skipped: true},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rv.Verdict != state.VerdictApproved {
		t.Errorf("verdict: got %q", rv.Verdict)
	}
	if rv.Summary != "looks good" {
		t.Errorf("summary: got %q", rv.Summary)
	}
}

func TestRun_ParsesChangesRequestedWithComments(t *testing.T) {
	repo := initRepo(t)

	body := `{"verdict":"CHANGES_REQUESTED","summary":"two issues","ac_check":[{"ac":"AC1","satisfied":false,"note":"missing"}],"comments":[{"file":"x.go","line":12,"severity":"blocker","comment":"nil deref"},{"file":"x.go","line":40,"severity":"nit","comment":"name"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		raw, _ := json.Marshal(body)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":`+string(raw)+`}}]}`)
	}))
	defer srv.Close()

	rv, err := Run(context.Background(), Options{
		Story:      makeStory(),
		Reviewer:   config.LLMConfig{BaseURL: srv.URL, APIKey: "k", Model: "m"},
		Role:       defaultReviewerRole,
		TargetRepo: repo,
		BaseRef:    "main",
		Round:      1,
		Verifier:   state.VerifierRecord{Round: 1, Skipped: true},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rv.Verdict != state.VerdictChangesRequested {
		t.Errorf("verdict: got %q", rv.Verdict)
	}
	if len(rv.Comments) != 2 {
		t.Errorf("comments: got %d", len(rv.Comments))
	}
	if len(rv.ACCheck) != 1 || rv.ACCheck[0].Satisfied {
		t.Errorf("ac_check parse: %+v", rv.ACCheck)
	}
}

func TestFormatFeedback_BlockersAndNitsAndAC(t *testing.T) {
	r := state.ReviewRecord{
		Verdict: state.VerdictChangesRequested,
		Summary: "needs work",
		ACCheck: []state.ACCheckItem{
			{AC: "AC1", Satisfied: false, Note: "missing inclusive bound"},
			{AC: "AC2", Satisfied: true, Commit: "a1b2c3"},
		},
		Comments: []state.ReviewComment{
			{File: "utils.go", Line: 25, Severity: "blocker", Comment: "off-by-one"},
			{File: "utils.go", Line: 18, Severity: "nit", Comment: "rename cnt -> count"},
		},
	}
	out := FormatFeedback(r)
	for _, want := range []string{
		"Reviewer requested changes",
		"needs work",
		"Blockers (must fix):",
		"utils.go:25 — off-by-one",
		"Nits (advisory):",
		"utils.go:18 — rename cnt -> count",
		"✗ AC1",
		"✓ AC2",
		"a1b2c3",
	} {
		if !contains(out, want) {
			t.Errorf("FormatFeedback missing %q\nGot:\n%s", want, out)
		}
	}
}

func TestFormatFeedback_ApprovedReturnsEmpty(t *testing.T) {
	r := state.ReviewRecord{Verdict: state.VerdictApproved, Summary: "lgtm"}
	if got := FormatFeedback(r); got != "" {
		t.Errorf("APPROVED should yield empty feedback, got %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > len(sub) && (indexOf(s, sub) >= 0)))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// keep these imports referenced even if all tests get gated/skipped during refactor
var _ = filepath.Join
var _ = os.Getenv
