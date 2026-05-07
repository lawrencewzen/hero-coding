// Package state persists per-story RunStats to JSON on disk so an
// interrupted dispatcher run can resume mid-story.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

func file(stateDir, storyKey string) string {
	return filepath.Join(stateDir, storyKey+".json")
}

// Load reads a saved Stats record. Returns (nil, nil) when the file does
// not exist (a fresh story). On corruption, the file is moved aside to
// `<file>.corrupt-<unix>` so a human can inspect it, and an error is
// returned — silently treating corruption as "no state" would clobber an
// in-progress run's history.
func Load(stateDir, storyKey string) (*Stats, error) {
	fp := file(stateDir, storyKey)
	raw, err := os.ReadFile(fp)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var s Stats
	if err := json.Unmarshal(raw, &s); err != nil {
		quarantine := fmt.Sprintf("%s.corrupt-%d", fp, time.Now().Unix())
		_ = os.Rename(fp, quarantine)
		return nil, fmt.Errorf("state file %s is corrupt (moved to %s): %w", fp, quarantine, err)
	}
	return &s, nil
}

// Write persists Stats atomically (tmp + rename).
func Write(stateDir, storyKey string, s *Stats) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	fp := file(stateDir, storyKey)
	tmp := fp + ".tmp"
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, fp)
}

// Clear removes the state file (no error if missing).
func Clear(stateDir, storyKey string) error {
	err := os.Remove(file(stateDir, storyKey))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
