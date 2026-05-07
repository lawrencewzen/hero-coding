// Package dispatcher orchestrates one or more user-story rounds:
// worker → verifier → judge, with worktree management and resumable state.
package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"hero-coding/internal/config"
	"hero-coding/internal/judge"
	"hero-coding/internal/role"
	"hero-coding/internal/state"
	"hero-coding/internal/story"
	"hero-coding/internal/verifier"
	"hero-coding/internal/worker"
)

var safeKey = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Paths bundles the on-disk layout the dispatcher reads/writes.
type Paths struct {
	Root      string
	Inbox     string
	Done      string
	Runs      string
	StateDir  string
	Worktrees string
	Traces    string
}

// DefaultPaths derives the standard layout under root.
func DefaultPaths(root string) Paths {
	return Paths{
		Root:      root,
		Inbox:     filepath.Join(root, "inbox"),
		Done:      filepath.Join(root, "done"),
		Runs:      filepath.Join(root, "runs"),
		StateDir:  filepath.Join(root, "runs", "state"),
		Worktrees: filepath.Join(root, "worktrees"),
		Traces:    filepath.Join(root, "runs", "traces"),
	}
}

// Dispatcher executes stories. Construct once, reuse across stories.
type Dispatcher struct {
	cfg       *config.Config
	paths     Paths
	roles     role.Roles
	worker    *worker.Worker
	workerCfg config.LLMConfig
	judgeCfg  config.LLMConfig
	log       *slog.Logger
}

// New builds a Dispatcher using role.Defaults(). Use NewWithRoles to
// override individual roles (e.g. swap in a stricter Worker prompt or
// pin a specific model per role).
func New(cfg *config.Config, paths Paths) (*Dispatcher, error) {
	return NewWithRoles(cfg, paths, role.Defaults())
}

func NewWithRoles(cfg *config.Config, paths Paths, roles role.Roles) (*Dispatcher, error) {
	workerCfg, err := cfg.LLMFor("worker")
	if err != nil {
		return nil, err
	}
	judgeCfg, err := cfg.LLMFor("judge")
	if err != nil {
		return nil, err
	}
	return &Dispatcher{
		cfg:       cfg,
		paths:     paths,
		roles:     roles,
		worker:    worker.New(workerCfg, roles.Worker),
		workerCfg: workerCfg,
		judgeCfg:  judgeCfg,
		log:       slog.Default(),
	}, nil
}

// RunOnce processes a single story file end-to-end.
func (d *Dispatcher) RunOnce(ctx context.Context, storyPath string) (*state.Stats, error) {
	s, err := story.Parse(storyPath)
	if err != nil {
		return nil, fmt.Errorf("parse story: %w", err)
	}
	sid := s.ID()
	branch := "hero/" + sid
	storyKey := safeKey.ReplaceAllString(sid, "_")

	prior, err := state.Load(d.paths.StateDir, storyKey)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	resume := prior != nil && prior.FinalStatus == "running"

	var stats *state.Stats
	var baseSha, worktreePath string
	startRound := 1
	var lastFeedback string

	defer func() {
		// Cleanup worktree unless the run is mid-flight (kept for resume).
		if worktreePath == "" {
			return
		}
		if stats == nil || stats.FinalStatus != "running" {
			cleanupWorktree(context.Background(), d.cfg.Target.Repo, worktreePath)
		}
	}()

	if resume {
		stats = prior
		if stats.Verifications == nil {
			stats.Verifications = []state.VerifierRecord{}
		}
		baseSha = prior.BaseSha
		worktreePath = prior.WorktreePath
		startRound = len(prior.Verdicts) + 1
		if len(prior.Verdicts) > 0 {
			lastFeedback = prior.Verdicts[len(prior.Verdicts)-1].Reason
		}
		d.log.Info("resuming story", "id", sid, "from_round", startRound)

		if !isWorktreeOnBranch(ctx, worktreePath, branch) {
			d.log.Warn("worktree missing/broken — rebuilding", "base", baseSha)
			_, wt, err := prepareWorktree(ctx, prepareOpts{
				repo: d.cfg.Target.Repo, baseRef: baseSha, branch: branch,
				storyKey: storyKey, worktrees: d.paths.Worktrees,
			})
			if err != nil {
				return nil, fmt.Errorf("rebuild worktree: %w", err)
			}
			worktreePath = wt
			stats.WorktreePath = wt
		} else if rescued, _ := rescueDirtyTree(ctx, worktreePath); rescued {
			d.log.Info("committed dirty changes from interrupted round")
		}
	} else {
		if prior != nil {
			_ = state.Clear(d.paths.StateDir, storyKey)
		}
		d.log.Info("starting story", "id", sid, "title", s.Frontmatter.Title)
		bs, wt, err := prepareWorktree(ctx, prepareOpts{
			repo: d.cfg.Target.Repo, baseRef: d.cfg.Target.BaseRef, branch: branch,
			storyKey: storyKey, worktrees: d.paths.Worktrees,
		})
		if err != nil {
			return nil, fmt.Errorf("prepare worktree: %w", err)
		}
		baseSha, worktreePath = bs, wt
		stats = &state.Stats{
			StoryID: sid, StoryTitle: s.Frontmatter.Title,
			Branch: branch, BaseRef: d.cfg.Target.BaseRef, BaseSha: baseSha,
			WorktreePath: worktreePath,
			Worker:       state.WorkerRef{BaseURL: d.workerCfg.BaseURL, Model: d.workerCfg.Model},
			Judge:        state.JudgeRef{BaseURL: d.judgeCfg.BaseURL, Model: d.judgeCfg.Model},
			StartedAt:    state.NowISO(),
			WorkerRuns:   []state.WorkerRunStats{},
			Verifications: []state.VerifierRecord{},
			Verdicts:     []state.VerdictRecord{},
			FinalStatus:  "running",
		}
		if err := state.Write(d.paths.StateDir, storyKey, stats); err != nil {
			return nil, err
		}
	}

	limit := d.cfg.MaxRetries
	if s.Frontmatter.MaxRetries != nil {
		limit = *s.Frontmatter.MaxRetries
	}
	outerStart := time.Now()

	for round := startRound; round <= limit; round++ {
		d.log.Info("round", "n", round, "limit", limit)
		w, err := d.worker.Run(ctx, worker.Options{
			Story: s, TargetRepo: worktreePath, Round: round,
			CaptainFeedback: lastFeedback,
			TraceDir:        d.paths.Traces,
		})
		if err != nil {
			return stats, fmt.Errorf("worker round %d: %w", round, err)
		}
		stats.WorkerRuns = append(stats.WorkerRuns, w)
		d.log.Info("worker done", "wall_ms", w.WallMs, "tools", w.ToolUseTotal, "kill", w.KillReason)

		if _, err := autoRescueCommit(ctx, worktreePath, round, s.Frontmatter.Scope); err != nil {
			d.log.Warn("auto-rescue failed", "err", err)
		}

		v, err := verifier.Run(ctx, verifier.Options{
			Story: s, TargetRepo: worktreePath, Round: round,
			DefaultCommands: d.cfg.DefaultVerifyCommands,
			Timeout:         d.cfg.VerifyTimeout, LogsDir: d.paths.Runs,
		})
		if err != nil {
			return stats, fmt.Errorf("verifier round %d: %w", round, err)
		}
		stats.Verifications = append(stats.Verifications, v)

		jv, err := judge.Run(ctx, judge.Options{
			Story: s, Judge: d.judgeCfg, Role: d.roles.Judge,
			TargetRepo: worktreePath,
			BaseRef:    baseSha, Round: round, Verifier: v,
		})
		if err != nil {
			return stats, fmt.Errorf("judge round %d: %w", round, err)
		}
		stats.Verdicts = append(stats.Verdicts, jv)
		d.log.Info("judge", "verdict", jv.Verdict, "reason", truncate(jv.Reason, 120))

		if jv.Verdict == "FAIL" {
			feedback := jv.Reason
			if !v.Skipped {
				feedback = jv.Reason + "\n\n" + verifier.Summarize(v)
			}
			lastFeedback = feedback
			if err := story.AppendJudgeFeedback(storyPath, round, feedback); err != nil {
				d.log.Warn("append judge feedback failed", "err", err)
			}
		}
		commits, _ := countCommits(ctx, worktreePath, baseSha)
		stats.Commits = commits
		_ = state.Write(d.paths.StateDir, storyKey, stats)

		if jv.Verdict == "PASS" {
			stats.FinalStatus = "done"
			break
		}
	}

	if stats.FinalStatus == "running" {
		stats.FinalStatus = "gave_up"
	}
	stats.FinishedAt = state.NowISO()
	stats.TotalWallMs += time.Since(outerStart).Milliseconds()
	_ = state.Write(d.paths.StateDir, storyKey, stats)

	runFile, err := state.WriteRun(d.paths.Runs, stats)
	if err != nil {
		return stats, err
	}
	d.log.Info("done", "status", stats.FinalStatus, "commits", stats.Commits, "wall_ms", stats.TotalWallMs, "log", runFile)

	if stats.FinalStatus == "done" {
		if err := os.MkdirAll(d.paths.Done, 0o755); err != nil {
			return stats, err
		}
		dest := filepath.Join(d.paths.Done, filepath.Base(storyPath))
		if err := os.Rename(storyPath, dest); err != nil {
			d.log.Warn("move story to done failed", "err", err)
		}
	}
	return stats, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
