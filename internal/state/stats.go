package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Stats is the per-story persistent record. Mirrors the TS RunStats shape
// so existing JSON state files (if any) remain readable.
type Stats struct {
	StoryID      string `json:"storyId"`
	StoryTitle   string `json:"storyTitle"`
	Branch       string `json:"branch"`
	BaseRef      string `json:"baseRef"`
	BaseSha      string `json:"baseSha"`
	WorktreePath string `json:"worktreePath"`

	Worker   WorkerRef   `json:"worker"`
	Reviewer ReviewerRef `json:"reviewer"`

	StartedAt   string `json:"startedAt"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	TotalWallMs int64  `json:"totalWallMs,omitempty"`

	WorkerRuns    []WorkerRunStats `json:"workerRuns"`
	Verifications []VerifierRecord `json:"verifications"`
	Reviews       []ReviewRecord   `json:"reviews"`

	Commits     int    `json:"commits"`
	FinalStatus string `json:"finalStatus"` // "done" | "gave_up" | "running"
}

type WorkerRef struct {
	BaseURL string `json:"baseUrl,omitempty"`
	Model   string `json:"model"`
}

type ReviewerRef struct {
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
}

type WorkerRunStats struct {
	Round         int            `json:"round"`
	WallMs        int64          `json:"wallMs"`
	ToolUseByName map[string]int `json:"toolUseByName"`
	ToolUseTotal  int            `json:"toolUseTotal"`
	TokensIn      int            `json:"tokensIn"`
	TokensOut     int            `json:"tokensOut"`
	ExitCode      int            `json:"exitCode"`
	KillReason    string         `json:"killReason,omitempty"`
}

// Verdict values produced by the Reviewer. APPROVED ships; CHANGES_REQUESTED
// bounces back to the worker with the comments below.
const (
	VerdictApproved          = "APPROVED"
	VerdictChangesRequested  = "CHANGES_REQUESTED"
)

// ReviewRecord captures one Reviewer round's output. Comments are intended
// to be fed back to the worker verbatim (see reviewer.FormatFeedback).
type ReviewRecord struct {
	Round             int             `json:"round"`
	Verdict           string          `json:"verdict"` // APPROVED | CHANGES_REQUESTED
	Summary           string          `json:"summary"`
	ACCheck           []ACCheckItem   `json:"acCheck,omitempty"`
	Comments          []ReviewComment `json:"comments,omitempty"`
	ReviewerWallMs    int64           `json:"reviewerWallMs"`
	VerifierAllPassed bool            `json:"verifierAllPassed,omitempty"`
	ShortCircuited    bool            `json:"shortCircuited,omitempty"`
}

// ACCheckItem records the Reviewer's per-acceptance-criterion verdict.
type ACCheckItem struct {
	AC        string `json:"ac"`
	Satisfied bool   `json:"satisfied"`
	Commit    string `json:"commit,omitempty"`
	Note      string `json:"note,omitempty"`
}

// ReviewComment is one structured code-review remark. severity == "blocker"
// is fed back as a must-fix; "nit" is informational and does not by itself
// cause CHANGES_REQUESTED.
type ReviewComment struct {
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity"` // "blocker" | "nit"
	Comment  string `json:"comment"`
}

type VerifierCommandRecord struct {
	Tier       string `json:"tier,omitempty"` // "default" for flat-list stories; named tier otherwise
	Cmd        string `json:"cmd"`
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
	TimedOut   bool   `json:"timedOut"`
	StdoutTail string `json:"stdoutTail"`
	StderrTail string `json:"stderrTail"`
	Skipped    bool   `json:"skipped,omitempty"` // true when an earlier tier failed and we skipped this one
}

type VerifierRecord struct {
	Round     int                     `json:"round"`
	Skipped   bool                    `json:"skipped"`
	AllPassed bool                    `json:"allPassed"`
	WallMs    int64                   `json:"wallMs"`
	Commands  []VerifierCommandRecord `json:"commands"`
	LogFile   string                  `json:"logFile,omitempty"`
}

// WriteRun writes the final run record to runsDir as a timestamped file
// and returns its path.
func WriteRun(runsDir string, s *Stats) (string, error) {
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return "", err
	}
	ts := strings.NewReplacer(":", "-", ".", "-").Replace(s.StartedAt)
	file := filepath.Join(runsDir, fmt.Sprintf("%s-%s.json", s.StoryID, ts))
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		return "", err
	}
	return file, nil
}

// NowISO returns RFC-3339 UTC, matching what the TS layer produced.
func NowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}
