package tools

import (
	"path/filepath"
	"sync"
)

// ReadTracker tracks which files have been read in the current session.
// Used by WriteFileTool to enforce read-before-write on existing files.
type ReadTracker struct {
	mu    sync.Mutex
	paths map[string]bool
}

func NewReadTracker() *ReadTracker {
	return &ReadTracker{paths: make(map[string]bool)}
}

// normalize resolves a path to its canonical form (symlinks resolved, absolute, cleaned).
func normalize(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	// EvalSymlinks also Cleans, and resolves aliases like /var → /private/var on macOS.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return filepath.Clean(abs)
}

// Record marks a file path as having been read.
func (rt *ReadTracker) Record(path string) {
	key := normalize(path)

	rt.mu.Lock()
	rt.paths[key] = true
	rt.mu.Unlock()
}

// HasRead reports whether the given path has been recorded as read.
func (rt *ReadTracker) HasRead(path string) bool {
	key := normalize(path)

	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.paths[key]
}
