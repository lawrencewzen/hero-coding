// Package verifier runs deterministic shell commands (tests, lint,
// typecheck, etc.) against a target repo and produces a VerifierRecord
// the Reviewer can consume.
package verifier

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"hero-coding/internal/state"
	"hero-coding/internal/story"
)

const tailBytes = 4 * 1024

// Options configures one verifier invocation.
type Options struct {
	Story           *story.Story
	TargetRepo      string
	Round           int
	DefaultCommands []string
	Timeout         time.Duration
	LogsDir         string
}

// Run executes the story's `verify:` tiers (or DefaultCommands fallback,
// wrapped into a single "default" tier).
//
// Tiers run in declaration order with FAIL-FAST between tiers: as soon as
// one tier has a failing command, all subsequent tiers are recorded as
// Skipped without executing. Within a tier every command runs (so the
// author sees every lint error at once, not just the first).
//
// TRUST MODEL: cmd strings come from the operator-controlled inbox or env
// — treated as trusted shell input. Do NOT pipe untrusted content (PR
// descriptions, web sources) into here without an allowlist.
func Run(ctx context.Context, opts Options) (state.VerifierRecord, error) {
	tiers := opts.Story.Frontmatter.Verify
	if len(tiers) == 0 && len(opts.DefaultCommands) > 0 {
		tiers = []story.VerifyTier{{Name: "default", Commands: opts.DefaultCommands}}
	}
	start := time.Now()

	if len(tiers) == 0 {
		return state.VerifierRecord{
			Round:     opts.Round,
			Skipped:   true,
			AllPassed: true,
			WallMs:    0,
			Commands:  []state.VerifierCommandRecord{},
		}, nil
	}

	totalCmds := 0
	for _, t := range tiers {
		totalCmds += len(t.Commands)
	}
	records := make([]state.VerifierCommandRecord, 0, totalCmds)
	logChunks := make([]string, 0, totalCmds)

	earlyFail := false
	for _, tier := range tiers {
		if earlyFail {
			// Mark every command in this and subsequent tiers as skipped — we
			// still want them visible in the record so feedback can mention
			// "tier X was skipped because tier Y failed first".
			for _, cmd := range tier.Commands {
				records = append(records, state.VerifierCommandRecord{
					Tier: tier.Name, Cmd: cmd, Skipped: true,
				})
			}
			continue
		}
		for _, cmd := range tier.Commands {
			r, full := runOne(ctx, cmd, opts.TargetRepo, opts.Timeout)
			r.Tier = tier.Name
			records = append(records, r)
			logChunks = append(logChunks,
				fmt.Sprintf("$ [%s] %s\n[exit=%d%s duration=%dms]\n--- stdout ---\n%s\n--- stderr ---\n%s\n",
					tier.Name, cmd, r.ExitCode, ifThen(r.TimedOut, " TIMEOUT", ""), r.DurationMs, full.Stdout, full.Stderr,
				))
			if r.ExitCode != 0 {
				earlyFail = true // 不立刻 break,先把本 tier 剩下的 cmd 也跑完
			}
		}
	}

	allPassed := true
	for _, r := range records {
		if r.Skipped {
			allPassed = false
			break
		}
		if r.ExitCode != 0 {
			allPassed = false
			break
		}
	}

	logFile := ""
	if opts.LogsDir != "" {
		if err := os.MkdirAll(opts.LogsDir, 0o755); err != nil {
			return state.VerifierRecord{}, err
		}
		ts := strings.NewReplacer(":", "-", ".", "-").Replace(time.Now().UTC().Format(time.RFC3339))
		logFile = filepath.Join(opts.LogsDir,
			fmt.Sprintf("%s-%s-verify-r%d.log", opts.Story.Frontmatter.ID, ts, opts.Round),
		)
		if err := os.WriteFile(logFile, []byte(strings.Join(logChunks, "\n")), 0o644); err != nil {
			return state.VerifierRecord{}, err
		}
	}

	return state.VerifierRecord{
		Round:     opts.Round,
		Skipped:   false,
		AllPassed: allPassed,
		WallMs:    time.Since(start).Milliseconds(),
		Commands:  records,
		LogFile:   logFile,
	}, nil
}

type fullOutput struct {
	Stdout string
	Stderr string
}

func runOne(parent context.Context, cmd, cwd string, timeout time.Duration) (state.VerifierCommandRecord, fullOutput) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = cwd
	env := append(os.Environ(), "CI=1")
	c.Env = env
	// Put the child in its own process group so we can SIGKILL the whole
	// tree on timeout — otherwise `sh -c "long-running"` may leave its
	// children behind.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	err := c.Run()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)

	exitCode := 0
	switch {
	case timedOut:
		exitCode = 124
		// Make sure we reaped the whole group.
		if c.Process != nil {
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		}
	case err == nil:
		exitCode = 0
	default:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
			if exitCode < 0 {
				exitCode = 128 // killed by signal
			}
		} else {
			exitCode = -1
			stderr.WriteString(fmt.Sprintf("\nspawn error: %s", err.Error()))
		}
	}

	return state.VerifierCommandRecord{
			Cmd:        cmd,
			ExitCode:   exitCode,
			DurationMs: time.Since(start).Milliseconds(),
			TimedOut:   timedOut,
			StdoutTail: tail(stdout.String()),
			StderrTail: tail(stderr.String()),
		}, fullOutput{
			Stdout: stdout.String(),
			Stderr: stderr.String(),
		}
}

func tail(s string) string {
	if len(s) <= tailBytes {
		return s
	}
	return fmt.Sprintf("…[truncated %dB]…\n%s", len(s)-tailBytes, s[len(s)-tailBytes:])
}

func ifThen[T any](cond bool, yes, no T) T {
	if cond {
		return yes
	}
	return no
}

// Summarize returns a one-block human/LLM-readable summary of the record,
// suitable for feedback to the worker or for the Reviewer prompt.
//
// Convention: silent on success (one short OK line, no per-command output)
// to keep reviewer prompt context clean — most rounds pass and dumping passing
// command output every time both wastes tokens and dilutes the LLM's
// attention from the diff. On failure, surface the first failed tier with
// per-command excerpts so the next worker round has actionable feedback.
func Summarize(v state.VerifierRecord) string {
	if v.Skipped {
		return "verifier skipped (no commands declared)"
	}
	if v.AllPassed {
		return fmt.Sprintf("verifier OK (%d cmd, %dms)", len(v.Commands), v.WallMs)
	}

	// Find the first tier that failed; any tier after that is just Skipped.
	failedTier := ""
	for _, c := range v.Commands {
		if !c.Skipped && c.ExitCode != 0 {
			failedTier = c.Tier
			break
		}
	}

	failedCount := 0
	skippedTiers := map[string]bool{}
	var lines []string
	for _, c := range v.Commands {
		if c.Skipped {
			skippedTiers[c.Tier] = true
			continue
		}
		if c.ExitCode == 0 {
			continue
		}
		failedCount++
		excerpt := strings.TrimSpace(lastNLines(firstNonEmpty(c.StderrTail, c.StdoutTail), 6))
		timeoutSuffix := ""
		if c.TimedOut {
			timeoutSuffix = " (timeout)"
		}
		tierTag := ""
		if c.Tier != "" && c.Tier != "default" {
			tierTag = fmt.Sprintf("[%s] ", c.Tier)
		}
		lines = append(lines, fmt.Sprintf("  • %s`%s` exit=%d%s\n%s", tierTag, c.Cmd, c.ExitCode, timeoutSuffix, excerpt))
	}

	header := fmt.Sprintf("verifier FAIL (%d cmd failed", failedCount)
	if failedTier != "" && failedTier != "default" {
		header += fmt.Sprintf(" in tier %q", failedTier)
	}
	header += "):"
	if len(skippedTiers) > 0 {
		names := make([]string, 0, len(skippedTiers))
		for n := range skippedTiers {
			names = append(names, n)
		}
		header += fmt.Sprintf("\n  (subsequent tiers skipped: %s)", strings.Join(names, ", "))
	}
	return header + "\n" + strings.Join(lines, "\n")
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func lastNLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
