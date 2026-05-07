package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const bashTimeout = 120 * time.Second

// BashTool executes arbitrary shell commands.
type BashTool struct {
	WorkDir      string // session's current project directory; used as cmd.Dir
	GetEscalated func() []string
}

func (t *BashTool) Definition() ToolDef {
	return ToolDef{
		Type: "function",
		Function: FunctionDef{
			Name: "bash",
			Description: "Execute a shell command and return combined stdout and stderr output. Each call starts a fresh, stateless shell rooted at the project directory.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The shell command to execute.",
					},
					"timeout": map[string]any{
						"type":        "integer",
						"description": "Timeout in milliseconds. Default 120000 (120s), max 600000 (600s).",
					},
				},
				"required": []string{"command"},
			},
		},
	}
}

func (t *BashTool) Execute(ctx context.Context, args map[string]any) (result string, err error) {
	defer recoverExecute(&err)

	command, err := getRequiredStringArg(args, "command")
	if err != nil {
		return "", err
	}

	// Pre-check: if workDir is set, scan for absolute paths outside sandbox.
	if t.WorkDir != "" {
		if err := bashSandboxCheck(command, t.WorkDir, t.GetEscalated); err != nil {
			return "", err
		}
	}

	// Determine timeout: use caller-supplied value if present, otherwise default.
	timeout := bashTimeout
	if raw, ok := args["timeout"]; ok {
		if ms, ok := toInt(raw); ok {
			// Clamp to [1000, 600000] ms.
			if ms < 1000 {
				ms = 1000
			}
			if ms > 600000 {
				ms = 600000
			}
			timeout = time.Duration(ms) * time.Millisecond
		}
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command("bash", "-lc", command)
	if t.WorkDir != "" {
		cmd.Dir = t.WorkDir
	}
	// Put bash in its own process group so we can kill all children on timeout
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start bash: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var runErr error
	select {
	case runErr = <-done:
		// completed normally (exit 0 or non-zero)
	case <-timeoutCtx.Done():
		// kill the entire process group
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
		if ctx.Err() != nil {
			runErr = fmt.Errorf("bash command cancelled: %w", ctx.Err())
		} else {
			runErr = fmt.Errorf("bash command timed out after %s", timeout)
		}
	}

	result = truncateString(output.String(), maxToolOutputChars)
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

// bashSandboxCheck scans command for absolute path tokens that escape the sandbox.
// System directories (/bin, /usr, /lib, /proc, /sys, /dev, /tmp, /run) are exempt.
// Returns a SANDBOX_ESCAPE error listing the offending paths.
func bashSandboxCheck(command, workDir string, getEscalated func() []string) error {
	var escalated []string
	if getEscalated != nil {
		escalated = getEscalated()
	}

	// Regex: tokens starting with / that look like paths
	pathRe := regexp.MustCompile(`(?:^|[\s"'=])(\/[^\s"'<>&|;:(){}\[\],*?!\\]+)`)
	matches := pathRe.FindAllStringSubmatch(command, -1)

	exemptPrefixes := []string{
		"/bin/", "/sbin/", "/usr/", "/lib/", "/lib64/",
		"/proc/", "/sys/", "/dev/", "/tmp/", "/var/tmp/", "/run/",
		"/bin", "/sbin", "/usr", "/lib", "/lib64",
		"/proc", "/sys", "/dev", "/tmp", "/var/tmp", "/run",
	}

	cleanWorkDir := filepath.Clean(workDir)
	var offenders []string
	seen := map[string]bool{}

	for _, m := range matches {
		p := filepath.Clean(m[1])
		if seen[p] {
			continue
		}
		seen[p] = true

		// Exempt: within workDir
		if p == cleanWorkDir || strings.HasPrefix(p, cleanWorkDir+"/") {
			continue
		}
		// Exempt: system paths
		exempt := false
		for _, ep := range exemptPrefixes {
			if p == ep || strings.HasPrefix(p, ep) {
				exempt = true
				break
			}
		}
		if exempt {
			continue
		}
		// Exempt: escalated paths
		for _, ep := range escalated {
			clean := filepath.Clean(ep)
			if p == clean || strings.HasPrefix(p, clean+"/") {
				exempt = true
				break
			}
		}
		if !exempt {
			offenders = append(offenders, p)
		}
	}

	if len(offenders) > 0 {
		return fmt.Errorf("SANDBOX_ESCAPE: command accesses paths outside your workspace: %s — call request_permission to request access", strings.Join(offenders, ", "))
	}
	return nil
}
