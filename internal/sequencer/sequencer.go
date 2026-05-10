// Package sequencer runs a set of inter-dependent user stories in
// dependency order. Given a directory containing N story files (typically
// produced by `hero plan`), Sequencer:
//
//  1. Parses every story; reads frontmatter.depends_on edges.
//  2. Topologically sorts the DAG. Refuses cycles or unknown deps.
//  3. Walks stories in order. Each is dispatched through the existing
//     dispatcher.RunOnce — the same Worker → Verifier → Reviewer loop a
//     standalone story would go through.
//  4. If a story fails (gave_up / error), every transitive descendant
//     is marked SKIPPED and not dispatched.
//
// Sequencer is intentionally serial. Concurrency at the story level
// would mean multiple workers fighting over the same target git repo.
// Layer-level parallelism is a future extension.
package sequencer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hero-coding/internal/state"
	"hero-coding/internal/story"
)

// Status describes the outcome of one story run.
type Status string

const (
	StatusPending Status = "pending"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped" // an upstream dependency failed
	StatusError   Status = "error"   // dispatcher returned err (infra problem)
)

// StoryResult is one story's outcome.
type StoryResult struct {
	ID     string
	Path   string
	Status Status
	Reason string // failure / skip explanation
}

// Result is the aggregate report from Run.
type Result struct {
	Order   []string      // ids in the order they were dispatched (or skipped)
	Results []StoryResult // one entry per story, same order as Order
}

// Runner is the function signature Sequencer expects from the dispatcher.
// It returns the final Stats record (with FinalStatus = "done" | "gave_up")
// and any infrastructure error. We keep this as an interface so tests can
// stub it without standing up a full dispatcher.
type Runner interface {
	RunOnce(ctx context.Context, storyPath string) (*state.Stats, error)
}

// Run discovers all *.md files under planDir, sorts them by depends_on,
// and dispatches each in order through r. Stories whose ancestors failed
// are marked SKIPPED. The function aggregates results; it does NOT abort
// on first failure (an unrelated branch of the DAG might still succeed).
func Run(ctx context.Context, planDir string, r Runner) (Result, error) {
	stories, err := discover(planDir)
	if err != nil {
		return Result{}, err
	}
	if len(stories) == 0 {
		return Result{}, fmt.Errorf("no stories found under %s", planDir)
	}

	order, err := topoSort(stories)
	if err != nil {
		return Result{}, err
	}

	byID := map[string]*story.Story{}
	for _, s := range stories {
		byID[s.Frontmatter.ID] = s
	}

	results := make([]StoryResult, 0, len(order))
	statusByID := make(map[string]Status, len(order))
	for _, id := range order {
		s := byID[id]
		if blockedBy := upstreamFailure(s, statusByID); blockedBy != "" {
			res := StoryResult{
				ID:     id,
				Path:   s.Filepath,
				Status: StatusSkipped,
				Reason: fmt.Sprintf("upstream %q did not pass", blockedBy),
			}
			results = append(results, res)
			statusByID[id] = StatusSkipped
			continue
		}

		stats, err := r.RunOnce(ctx, s.Filepath)
		switch {
		case err != nil:
			results = append(results, StoryResult{
				ID: id, Path: s.Filepath, Status: StatusError, Reason: err.Error(),
			})
			statusByID[id] = StatusError
		case stats == nil:
			results = append(results, StoryResult{
				ID: id, Path: s.Filepath, Status: StatusError, Reason: "dispatcher returned nil stats",
			})
			statusByID[id] = StatusError
		case stats.FinalStatus == "done":
			results = append(results, StoryResult{
				ID: id, Path: s.Filepath, Status: StatusDone,
			})
			statusByID[id] = StatusDone
		default:
			results = append(results, StoryResult{
				ID: id, Path: s.Filepath, Status: StatusFailed,
				Reason: fmt.Sprintf("dispatcher final status %q", stats.FinalStatus),
			})
			statusByID[id] = StatusFailed
		}
	}

	return Result{Order: order, Results: results}, nil
}

// upstreamFailure returns the first failing/skipped/errored ancestor's id,
// or "" if every dependency completed successfully. We rely on topo order:
// by the time a story is visited, every dep already has a status entry.
func upstreamFailure(s *story.Story, statusByID map[string]Status) string {
	for _, dep := range s.Frontmatter.DependsOn {
		switch statusByID[dep] {
		case StatusDone:
			continue
		default:
			return dep
		}
	}
	return ""
}

// discover loads every *.md file under planDir as a story. The manifest
// file (`_manifest.yaml`) is intentionally NOT consulted at runtime —
// the story files are the source of truth, and their frontmatter
// depends_on edges drive the DAG.
func discover(planDir string) ([]*story.Story, error) {
	entries, err := os.ReadDir(planDir)
	if err != nil {
		return nil, fmt.Errorf("read plan dir %s: %w", planDir, err)
	}
	out := make([]*story.Story, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		s, err := story.Parse(filepath.Join(planDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		out = append(out, s)
	}
	return out, nil
}

// topoSort returns story ids in a dependency-respecting order. Refuses
// cycles, unknown dependencies, and missing roots (every story depending
// on something).
func topoSort(stories []*story.Story) ([]string, error) {
	ids := make(map[string]bool, len(stories))
	for _, s := range stories {
		if ids[s.Frontmatter.ID] {
			return nil, fmt.Errorf("duplicate story id %q", s.Frontmatter.ID)
		}
		ids[s.Frontmatter.ID] = true
	}
	for _, s := range stories {
		for _, d := range s.Frontmatter.DependsOn {
			if !ids[d] {
				return nil, fmt.Errorf("story %q references unknown dependency %q", s.Frontmatter.ID, d)
			}
		}
	}

	// Kahn-style: start with all in-degree 0 nodes, repeatedly remove.
	indeg := make(map[string]int, len(stories))
	deps := make(map[string][]string, len(stories))
	dependents := make(map[string][]string, len(stories))
	for _, s := range stories {
		indeg[s.Frontmatter.ID] = len(s.Frontmatter.DependsOn)
		deps[s.Frontmatter.ID] = append([]string(nil), s.Frontmatter.DependsOn...)
		for _, d := range s.Frontmatter.DependsOn {
			dependents[d] = append(dependents[d], s.Frontmatter.ID)
		}
	}

	// Stable order: pick ready nodes in alphabetical id order to make the
	// dispatch order deterministic across runs.
	ready := []string{}
	for _, s := range stories {
		if indeg[s.Frontmatter.ID] == 0 {
			ready = append(ready, s.Frontmatter.ID)
		}
	}
	sort.Strings(ready)
	if len(ready) == 0 {
		return nil, fmt.Errorf("no root story (every story depends on something — cycle or missing entry point)")
	}

	out := make([]string, 0, len(stories))
	for len(ready) > 0 {
		head := ready[0]
		ready = ready[1:]
		out = append(out, head)
		// Sort dependents to keep ordering stable across map iteration.
		nextDeps := append([]string(nil), dependents[head]...)
		sort.Strings(nextDeps)
		for _, d := range nextDeps {
			indeg[d]--
			if indeg[d] == 0 {
				ready = insertSorted(ready, d)
			}
		}
	}
	if len(out) != len(stories) {
		return nil, fmt.Errorf("dependency cycle detected (sequenced %d of %d stories)", len(out), len(stories))
	}
	return out, nil
}

func insertSorted(s []string, v string) []string {
	i := sort.SearchStrings(s, v)
	s = append(s, "")
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// FormatReport renders a Result as a short human-readable block — used
// by the CLI on completion and convenient for tests.
func FormatReport(r Result) string {
	var b strings.Builder
	doneN, failedN, skippedN, errorN := 0, 0, 0, 0
	for _, s := range r.Results {
		switch s.Status {
		case StatusDone:
			doneN++
		case StatusFailed:
			failedN++
		case StatusSkipped:
			skippedN++
		case StatusError:
			errorN++
		}
	}
	fmt.Fprintf(&b, "sequencer: %d done, %d failed, %d skipped, %d error\n",
		doneN, failedN, skippedN, errorN)
	for _, s := range r.Results {
		fmt.Fprintf(&b, "  %s  %-8s %s", iconFor(s.Status), s.Status, s.ID)
		if s.Reason != "" {
			fmt.Fprintf(&b, "  — %s", s.Reason)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func iconFor(s Status) string {
	switch s {
	case StatusDone:
		return "✓"
	case StatusFailed:
		return "✗"
	case StatusSkipped:
		return "↷"
	case StatusError:
		return "!"
	default:
		return "·"
	}
}
