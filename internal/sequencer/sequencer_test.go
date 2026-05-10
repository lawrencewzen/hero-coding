package sequencer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hero-coding/internal/state"
)

// fakeRunner records which stories were dispatched and lets each test
// decide their outcome.
type fakeRunner struct {
	calls    []string
	finalFor map[string]string // id -> "done" | "gave_up" | "" for nil-stats / err
	errFor   map[string]error
}

func (f *fakeRunner) RunOnce(ctx context.Context, storyPath string) (*state.Stats, error) {
	id := strings.TrimSuffix(filepath.Base(storyPath), ".md")
	f.calls = append(f.calls, id)
	if err, ok := f.errFor[id]; ok && err != nil {
		return nil, err
	}
	final, ok := f.finalFor[id]
	if !ok {
		final = "done" // default success
	}
	if final == "" {
		return nil, nil // simulate dispatcher returning nil stats
	}
	return &state.Stats{StoryID: id, FinalStatus: final}, nil
}

func writeStoryFile(t *testing.T, dir, id, title string, deps []string) string {
	t.Helper()
	depsYAML := "[]"
	if len(deps) > 0 {
		quoted := make([]string, len(deps))
		for i, d := range deps {
			quoted[i] = fmt.Sprintf("%q", d)
		}
		depsYAML = "[" + strings.Join(quoted, ", ") + "]"
	}
	body := fmt.Sprintf(`---
id: %s
title: %s
priority: normal
depends_on: %s
---

## Goal
test
`, id, title, depsYAML)
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type spec struct {
	ID    string
	Title string
	Deps  []string
}

func makePlanDir(t *testing.T, stories []spec) string {
	t.Helper()
	dir := t.TempDir()
	for _, s := range stories {
		writeStoryFile(t, dir, s.ID, s.Title, s.Deps)
	}
	return dir
}

func TestRun_LinearChain_AllPass(t *testing.T) {
	dir := makePlanDir(t, []spec{
		{"us-001", "first", nil},
		{"us-002", "second", []string{"us-001"}},
		{"us-003", "third", []string{"us-002"}},
	})
	r := &fakeRunner{}
	res, err := Run(context.Background(), dir, r)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"us-001", "us-002", "us-003"}
	if !equalSlice(r.calls, want) {
		t.Errorf("dispatch order: got %v want %v", r.calls, want)
	}
	for _, sr := range res.Results {
		if sr.Status != StatusDone {
			t.Errorf("%s: status %q", sr.ID, sr.Status)
		}
	}
}

func TestRun_FailurePropagatesAsSkipped(t *testing.T) {
	dir := makePlanDir(t, []spec{
		{"us-001", "first", nil},
		{"us-002", "second", []string{"us-001"}},
		{"us-003", "third", []string{"us-002"}},
		{"us-004", "independent", nil}, // independent — should still run
	})
	r := &fakeRunner{
		finalFor: map[string]string{"us-001": "gave_up"},
	}
	res, err := Run(context.Background(), dir, r)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	statusByID := map[string]Status{}
	for _, sr := range res.Results {
		statusByID[sr.ID] = sr.Status
	}
	if statusByID["us-001"] != StatusFailed {
		t.Errorf("us-001 want failed, got %s", statusByID["us-001"])
	}
	if statusByID["us-002"] != StatusSkipped || statusByID["us-003"] != StatusSkipped {
		t.Errorf("downstream stories should be skipped: %v", statusByID)
	}
	if statusByID["us-004"] != StatusDone {
		t.Errorf("independent story should still run: %s", statusByID["us-004"])
	}
	// Skipped stories must not have been dispatched.
	for _, c := range r.calls {
		if c == "us-002" || c == "us-003" {
			t.Errorf("skipped story %s should NOT have been dispatched", c)
		}
	}
}

func TestRun_DispatcherError_PropagatesDownstream(t *testing.T) {
	dir := makePlanDir(t, []spec{
		{"us-001", "first", nil},
		{"us-002", "second", []string{"us-001"}},
	})
	r := &fakeRunner{errFor: map[string]error{"us-001": fmt.Errorf("git failure")}}
	res, _ := Run(context.Background(), dir, r)
	got := map[string]Status{}
	for _, sr := range res.Results {
		got[sr.ID] = sr.Status
	}
	if got["us-001"] != StatusError {
		t.Errorf("us-001 want error, got %s", got["us-001"])
	}
	if got["us-002"] != StatusSkipped {
		t.Errorf("downstream should skip on upstream error, got %s", got["us-002"])
	}
}

func TestRun_DiamondDAG(t *testing.T) {
	// us-001 → us-002 ↘
	//                  us-004
	// us-001 → us-003 ↗
	dir := makePlanDir(t, []spec{
		{"us-001", "root", nil},
		{"us-002", "left", []string{"us-001"}},
		{"us-003", "right", []string{"us-001"}},
		{"us-004", "merge", []string{"us-002", "us-003"}},
	})
	r := &fakeRunner{}
	_, err := Run(context.Background(), dir, r)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Order constraints: us-001 first, us-004 last.
	if r.calls[0] != "us-001" {
		t.Errorf("first call should be us-001, got %s", r.calls[0])
	}
	if r.calls[len(r.calls)-1] != "us-004" {
		t.Errorf("last call should be us-004, got %s", r.calls[len(r.calls)-1])
	}
	// us-002 and us-003 both come after us-001 and before us-004.
	idx := map[string]int{}
	for i, c := range r.calls {
		idx[c] = i
	}
	if !(idx["us-001"] < idx["us-002"] && idx["us-001"] < idx["us-003"] &&
		idx["us-002"] < idx["us-004"] && idx["us-003"] < idx["us-004"]) {
		t.Errorf("DAG order violated: %v", r.calls)
	}
}

func TestRun_RejectsCycle(t *testing.T) {
	dir := makePlanDir(t, []spec{
		{"us-001", "a", []string{"us-002"}},
		{"us-002", "b", []string{"us-001"}},
	})
	_, err := Run(context.Background(), dir, &fakeRunner{})
	if err == nil {
		t.Fatal("expected cycle / no-root error")
	}
}

func TestRun_RejectsUnknownDependency(t *testing.T) {
	dir := makePlanDir(t, []spec{
		{"us-001", "a", []string{"us-999"}},
	})
	_, err := Run(context.Background(), dir, &fakeRunner{})
	if err == nil {
		t.Fatal("expected unknown-dep error")
	}
	if !strings.Contains(err.Error(), "unknown dependency") {
		t.Errorf("error wording: %v", err)
	}
}

func TestRun_NoStoriesError(t *testing.T) {
	dir := t.TempDir()
	_, err := Run(context.Background(), dir, &fakeRunner{})
	if err == nil {
		t.Fatal("expected no-stories error")
	}
}

func TestFormatReport_RendersAllStatuses(t *testing.T) {
	r := Result{
		Order: []string{"us-001", "us-002", "us-003", "us-004"},
		Results: []StoryResult{
			{ID: "us-001", Status: StatusDone},
			{ID: "us-002", Status: StatusFailed, Reason: "judge said no"},
			{ID: "us-003", Status: StatusSkipped, Reason: `upstream "us-002" did not pass`},
			{ID: "us-004", Status: StatusError, Reason: "git oops"},
		},
	}
	out := FormatReport(r)
	for _, want := range []string{"1 done", "1 failed", "1 skipped", "1 error", "us-002", "judge said no", "upstream"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q in:\n%s", want, out)
		}
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
