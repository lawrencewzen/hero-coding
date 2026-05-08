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

	Worker WorkerRef `json:"worker"`
	Judge  JudgeRef  `json:"judge"`

	StartedAt   string `json:"startedAt"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	TotalWallMs int64  `json:"totalWallMs,omitempty"`

	WorkerRuns    []WorkerRunStats `json:"workerRuns"`
	Verifications []VerifierRecord `json:"verifications"`
	Verdicts      []VerdictRecord  `json:"verdicts"`

	Commits     int    `json:"commits"`
	FinalStatus string `json:"finalStatus"` // "done" | "gave_up" | "running"
}

type WorkerRef struct {
	BaseURL string `json:"baseUrl,omitempty"`
	Model   string `json:"model"`
}

type JudgeRef struct {
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

type VerdictRecord struct {
	Round             int    `json:"round"`
	Verdict           string `json:"verdict"` // PASS | FAIL
	Reason            string `json:"reason"`
	JudgeWallMs       int64  `json:"judgeWallMs"`
	VerifierAllPassed bool   `json:"verifierAllPassed,omitempty"`
	ShortCircuited    bool   `json:"shortCircuited,omitempty"`
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
