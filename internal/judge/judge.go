// Package judge produces a PASS/FAIL verdict over a worker round by
// combining deterministic Verifier output and an LLM review of the diff.
package judge

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
	Judge      config.LLMConfig
	Role       role.Role // SystemPrompt drives the verdict prompt; Model overrides Judge.Model when set
	TargetRepo string
	BaseRef    string
	Round      int
	Verifier   state.VerifierRecord
}

// Run produces a Verdict. If the verifier ran any commands and at least
// one failed, returns FAIL deterministically without calling the LLM.
func Run(ctx context.Context, opts Options) (state.VerdictRecord, error) {
	if !opts.Verifier.Skipped && !opts.Verifier.AllPassed {
		return state.VerdictRecord{
			Round:             opts.Round,
			Verdict:           "FAIL",
			Reason:            verifier.Summarize(opts.Verifier),
			JudgeWallMs:       0,
			VerifierAllPassed: false,
			ShortCircuited:    true,
		}, nil
	}

	gitCtx, err := collectGitContext(ctx, opts.TargetRepo, opts.BaseRef)
	if err != nil {
		return state.VerdictRecord{}, fmt.Errorf("git context: %w", err)
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

	cfg := opts.Judge
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
		return state.VerdictRecord{}, fmt.Errorf("judge llm: %w", err)
	}

	parsed, raw, ok := parseVerdict(resp)
	if !ok {
		return state.VerdictRecord{
			Round:             opts.Round,
			Verdict:           "FAIL",
			Reason:            fmt.Sprintf("Judge returned malformed output: %s", truncate(raw, 500)),
			JudgeWallMs:       wall,
			VerifierAllPassed: !opts.Verifier.Skipped && opts.Verifier.AllPassed,
			ShortCircuited:    false,
		}, nil
	}

	return state.VerdictRecord{
		Round:             opts.Round,
		Verdict:           parsed.Verdict,
		Reason:            parsed.Reason,
		JudgeWallMs:       wall,
		VerifierAllPassed: !opts.Verifier.Skipped && opts.Verifier.AllPassed,
		ShortCircuited:    false,
	}, nil
}

type verdictWire struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

func parseVerdict(msg *agent.ChatMessage) (verdictWire, string, bool) {
	raw := strings.TrimSpace(extractText(msg.Content))
	if raw == "" {
		return verdictWire{}, "", false
	}
	// Some providers may wrap JSON in code fences despite response_format.
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var v verdictWire
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return verdictWire{}, raw, false
	}
	v.Verdict = strings.ToUpper(strings.TrimSpace(v.Verdict))
	if v.Verdict != "PASS" && v.Verdict != "FAIL" {
		return verdictWire{}, raw, false
	}
	return v, raw, true
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

// formatVerifier produces the verifier block for the Judge's prompt.
// Because Run() short-circuits on verifier failure, this function only
// ever sees Skipped or AllPassed records. We collapse passing runs to a
// single line to keep the Judge's context clean — most rounds pass and
// listing every passing command burns tokens without changing the verdict.
func formatVerifier(v state.VerifierRecord) string {
	if v.Skipped {
		return "(no verifier commands declared — judging on diff alone)"
	}
	tiers := tierNamesInOrder(v.Commands)
	if len(tiers) == 1 && tiers[0] == "default" {
		return fmt.Sprintf("verifier OK (%d cmd, %dms)", len(v.Commands), v.WallMs)
	}
	return fmt.Sprintf("verifier OK (%d cmd across tiers %s, %dms)",
		len(v.Commands), strings.Join(tiers, " → "), v.WallMs)
}

// tierNamesInOrder returns each distinct Tier value in first-seen order,
// matching the declaration order in the story.
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

