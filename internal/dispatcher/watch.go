package dispatcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Watch processes the inbox backlog and then keeps watching for new .md
// files. Concurrency is bounded by cfg.MaxParallel via a buffered chan
// semaphore. Returns when ctx is cancelled.
func (d *Dispatcher) Watch(ctx context.Context) error {
	for _, dir := range []string{d.paths.Inbox, d.paths.Done, d.paths.Runs} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	sem := make(chan struct{}, d.cfg.MaxParallel)
	var inflight sync.Map

	dispatch := func(path string) {
		if !strings.HasSuffix(path, ".md") {
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return
		}
		if _, loaded := inflight.LoadOrStore(abs, struct{}{}); loaded {
			return
		}
		go func() {
			defer inflight.Delete(abs)
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			if _, err := d.RunOnce(ctx, abs); err != nil {
				d.log.Error("dispatch error", "file", filepath.Base(abs), "err", err)
			}
		}()
	}

	// Backlog: process anything already in inbox.
	entries, err := os.ReadDir(d.paths.Inbox)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		dispatch(filepath.Join(d.paths.Inbox, e.Name()))
	}

	d.log.Info("watching", "inbox", d.paths.Inbox, "max_parallel", d.cfg.MaxParallel)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	if err := w.Add(d.paths.Inbox); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Op&fsnotify.Create != 0 {
				dispatch(ev.Name)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			d.log.Error("fsnotify error", "err", err)
		}
	}
}
