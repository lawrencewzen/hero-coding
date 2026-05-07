// Package verifier runs deterministic shell commands (tests, lint,
// typecheck, etc.) against a target repo and produces a VerifierRecord
// the Judge can consume.
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

// Run executes the story's `verify:` commands (or DefaultCommands fallback).
//
// TRUST MODEL: cmd strings come from the operator-controlled inbox or env
// — treated as trusted shell input. Do NOT pipe untrusted content (PR
// descriptions, web sources) into here without an allowlist.
func Run(ctx context.Context, opts Options) (state.VerifierRecord, error) {
	cmds := opts.Story.Frontmatter.Verify
	if len(cmds) == 0 {
		cmds = opts.DefaultCommands
	}
	start := time.Now()

	if len(cmds) == 0 {
		return state.VerifierRecord{
			Round:     opts.Round,
			Skipped:   true,
			AllPassed: true,
			WallMs:    0,
			Commands:  []state.VerifierCommandRecord{},
		}, nil
	}

	records := make([]state.VerifierCommandRecord, 0, len(cmds))
	logChunks := make([]string, 0, len(cmds))

	for _, cmd := range cmds {
		r, full := runOne(ctx, cmd, opts.TargetRepo, opts.Timeout)
		records = append(records, r)
		logChunks = append(logChunks,
			fmt.Sprintf("$ %s\n[exit=%d%s duration=%dms]\n--- stdout ---\n%s\n--- stderr ---\n%s\n",
				cmd, r.ExitCode, ifThen(r.TimedOut, " TIMEOUT", ""), r.DurationMs, full.Stdout, full.Stderr,
			))
	}

	allPassed := true
	for _, r := range records {
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
// suitable for feedback to the worker or for the Judge prompt.
func Summarize(v state.VerifierRecord) string {
	if v.Skipped {
		return "verifier skipped (no commands declared)"
	}
	if v.AllPassed {
		return fmt.Sprintf("verifier OK (%d cmd, %dms)", len(v.Commands), v.WallMs)
	}
	failed := 0
	var lines []string
	for _, c := range v.Commands {
		if c.ExitCode == 0 {
			continue
		}
		failed++
		excerpt := strings.TrimSpace(lastNLines(firstNonEmpty(c.StderrTail, c.StdoutTail), 6))
		timeoutSuffix := ""
		if c.TimedOut {
			timeoutSuffix = " (timeout)"
		}
		lines = append(lines, fmt.Sprintf("  • `%s` exit=%d%s\n%s", c.Cmd, c.ExitCode, timeoutSuffix, excerpt))
	}
	return fmt.Sprintf("verifier FAIL (%d/%d cmd failed):\n%s", failed, len(v.Commands), strings.Join(lines, "\n"))
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
