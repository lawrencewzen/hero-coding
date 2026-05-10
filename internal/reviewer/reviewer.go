// Package reviewer produces an APPROVED / CHANGES_REQUESTED verdict over a
// worker round by combining deterministic Verifier output and an LLM code
// review of the diff. Reviewer is the FINAL gate: APPROVED ships the
// story, CHANGES_REQUESTED bounces it back to the worker with concrete
// comments to address.
//
// Reviewer differs from a "judge" in two ways:
//
//  1. It examines the diff for code quality (correctness, design, scope,
//     maintainability) — not just whether Acceptance Criteria are visibly
//     ticked off.
//  2. Its output carries structured per-comment feedback (file/line/severity)
//     that is fed back to the worker verbatim, not just a single reason
//     string.
package reviewer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"hero-coding/internal/agent"
	"hero-coding/internal/config"
	"hero-coding/internal/role"
	"hero-coding/internal/state"
	"hero-coding/internal/story"
	"hero-coding/internal/verifier"
)

// Options is the per-call input to Run.
type Options struct {
	Story      *story.Story
	Reviewer   config.LLMConfig
	Role       role.Role // SystemPrompt drives the review prompt; Model overrides Reviewer.Model when set
	TargetRepo string
	BaseRef    string
	Round      int
	Verifier   state.VerifierRecord
}

// Run produces a ReviewRecord. If the verifier ran any commands and at least
// one failed, it returns CHANGES_REQUESTED deterministically without calling
// the LLM — failed tests are an objective bounce.
func Run(ctx context.Context, opts Options) (state.ReviewRecord, error) {
	if !opts.Verifier.Skipped && !opts.Verifier.AllPassed {
		return state.ReviewRecord{
			Round:             opts.Round,
			Verdict:           state.VerdictChangesRequested,
			Summary:           verifier.Summarize(opts.Verifier),
			ReviewerWallMs:    0,
			VerifierAllPassed: false,
			ShortCircuited:    true,
		}, nil
	}

	gitCtx, err := collectGitContext(ctx, opts.TargetRepo, opts.BaseRef)
	if err != nil {
		return state.ReviewRecord{}, fmt.Errorf("git context: %w", err)
	}

	user := strings.Join([]string{
		"# User Story",
		opts.Story.Body,
		"",
		"# Verifier Results (deterministic)",
		formatVerifier(opts.Verifier),
		"",
		"# Git Context",
		gitCtx,
	}, "\n")

	cfg := opts.Reviewer
	if opts.Role.Model != "" {
		cfg.Model = opts.Role.Model
	}
	client := agent.NewLLMClient(&cfg)
	temp := 0.1
	jsonObj := "json_object"
	chatOpts := agent.ChatOptions{
		Temperature:    &temp,
		ResponseFormat: &jsonObj,
	}
	msgs := []agent.ChatMessage{
		{Role: "system", Content: opts.Role.SystemPrompt},
		{Role: "user", Content: user},
	}

	start := time.Now()
	resp, err := client.ChatWithOptions(ctx, msgs, nil, chatOpts)
	wall := time.Since(start).Milliseconds()
	if err != nil {
		return state.ReviewRecord{}, fmt.Errorf("reviewer llm: %w", err)
	}

	parsed, raw, ok := parseReview(resp)
	if !ok {
		return state.ReviewRecord{
			Round:             opts.Round,
			Verdict:           state.VerdictChangesRequested,
			Summary:           fmt.Sprintf("Reviewer returned malformed output: %s", truncate(raw, 500)),
			ReviewerWallMs:    wall,
			VerifierAllPassed: !opts.Verifier.Skipped && opts.Verifier.AllPassed,
			ShortCircuited:    false,
		}, nil
	}

	return state.ReviewRecord{
		Round:             opts.Round,
		Verdict:           parsed.Verdict,
		Summary:           parsed.Summary,
		Comments:          parsed.Comments,
		ACCheck:           parsed.ACCheck,
		ReviewerWallMs:    wall,
		VerifierAllPassed: !opts.Verifier.Skipped && opts.Verifier.AllPassed,
		ShortCircuited:    false,
	}, nil
}

// reviewWire is the wire schema we expect the LLM to emit.
type reviewWire struct {
	Verdict  string                `json:"verdict"`
	Summary  string                `json:"summary"`
	ACCheck  []state.ACCheckItem   `json:"ac_check"`
	Comments []state.ReviewComment `json:"comments"`
}

func parseReview(msg *agent.ChatMessage) (reviewWire, string, bool) {
	raw := strings.TrimSpace(extractText(msg.Content))
	if raw == "" {
		return reviewWire{}, "", false
	}
	// Some providers may wrap JSON in code fences despite response_format.
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var v reviewWire
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return reviewWire{}, raw, false
	}
	v.Verdict = strings.ToUpper(strings.TrimSpace(v.Verdict))
	if v.Verdict != state.VerdictApproved && v.Verdict != state.VerdictChangesRequested {
		return reviewWire{}, raw, false
	}
	return v, raw, true
}

// FormatFeedback turns a CHANGES_REQUESTED ReviewRecord into a worker-ready
// feedback block: one section per dimension (AC status, blockers, nits,
// summary). The output gets appended to the story's Captain Feedback area
// and prepended to the next round's user prompt.
//
// Returns "" for APPROVED records — there is nothing to feed back.
func FormatFeedback(r state.ReviewRecord) string {
	if r.Verdict == state.VerdictApproved {
		return ""
	}
	var b strings.Builder
	b.WriteString("Reviewer requested changes.")
	if r.Summary != "" {
		b.WriteString("\n\nSummary: ")
		b.WriteString(r.Summary)
	}

	var blockers, nits []state.ReviewComment
	for _, c := range r.Comments {
		switch strings.ToLower(c.Severity) {
		case "blocker":
			blockers = append(blockers, c)
		default:
			nits = append(nits, c)
		}
	}

	if len(blockers) > 0 {
		b.WriteString("\n\nBlockers (must fix):")
		for _, c := range blockers {
			b.WriteString("\n  - ")
			b.WriteString(formatCommentLine(c))
		}
	}
	if len(nits) > 0 {
		b.WriteString("\n\nNits (advisory):")
		for _, c := range nits {
			b.WriteString("\n  - ")
			b.WriteString(formatCommentLine(c))
		}
	}
	if len(r.ACCheck) > 0 {
		b.WriteString("\n\nAcceptance Criteria status:")
		for _, ac := range r.ACCheck {
			mark := "✓"
			if !ac.Satisfied {
				mark = "✗"
			}
			line := fmt.Sprintf("\n  %s %s", mark, ac.AC)
			if ac.Commit != "" && ac.Satisfied {
				line += fmt.Sprintf(" (%s)", ac.Commit)
			}
			if ac.Note != "" {
				line += fmt.Sprintf(" — %s", ac.Note)
			}
			b.WriteString(line)
		}
	}
	return b.String()
}

func formatCommentLine(c state.ReviewComment) string {
	loc := c.File
	if c.Line > 0 {
		loc = fmt.Sprintf("%s:%d", c.File, c.Line)
	}
	if loc == "" {
		return c.Comment
	}
	return fmt.Sprintf("%s — %s", loc, c.Comment)
}

func extractText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []agent.ContentPart:
		var b strings.Builder
		for _, p := range v {
			if p.Type == "text" || p.Type == "" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

// formatVerifier produces the verifier block for the Reviewer's prompt.
// Because Run() short-circuits on verifier failure, this function only
// ever sees Skipped or AllPassed records.
func formatVerifier(v state.VerifierRecord) string {
	if v.Skipped {
		return "(no verifier commands declared — reviewing on diff alone)"
	}
	tiers := tierNamesInOrder(v.Commands)
	if len(tiers) == 1 && tiers[0] == "default" {
		return fmt.Sprintf("verifier OK (%d cmd, %dms)", len(v.Commands), v.WallMs)
	}
	return fmt.Sprintf("verifier OK (%d cmd across tiers %s, %dms)",
		len(v.Commands), strings.Join(tiers, " → "), v.WallMs)
}

func tierNamesInOrder(cmds []state.VerifierCommandRecord) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	for _, c := range cmds {
		if c.Tier == "" || seen[c.Tier] {
			continue
		}
		seen[c.Tier] = true
		out = append(out, c.Tier)
	}
	return out
}

func collectGitContext(ctx context.Context, repo, baseRef string) (string, error) {
	log, err := git(ctx, repo, "log", "--reverse", "--pretty=format:### %h %s%n%n%b%n", baseRef+"..HEAD")
	if err != nil {
		return "", err
	}
	statDiff, err := git(ctx, repo, "diff", baseRef+"..HEAD", "--stat")
	if err != nil {
		return "", err
	}
	full, err := git(ctx, repo, "diff", baseRef+"..HEAD")
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"## Commits since base",
		orPlaceholder(log, "(no commits)"),
		"",
		"## Diff stat",
		orPlaceholder(statDiff, "(empty)"),
		"",
		"## Full diff",
		orPlaceholder(full, "(empty)"),
	}, "\n"), nil
}

func git(ctx context.Context, repo string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = repo
	var out, errBuf bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errBuf
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

func orPlaceholder(s, placeholder string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return placeholder
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
