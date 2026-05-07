package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func git(ctx context.Context, repo string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = repo
	var out, errBuf bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errBuf
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func removeBranchIfExists(ctx context.Context, repo, branch string) error {
	out, err := git(ctx, repo, "branch", "--list", branch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	_, err = git(ctx, repo, "branch", "-D", branch)
	return err
}

type prepareOpts struct {
	repo      string
	baseRef   string
	branch    string
	storyKey  string
	worktrees string
}

func prepareWorktree(ctx context.Context, o prepareOpts) (baseSha, worktreePath string, err error) {
	baseSha, err = git(ctx, o.repo, "rev-parse", o.baseRef)
	if err != nil {
		return "", "", err
	}
	worktreePath = filepath.Join(o.worktrees, o.storyKey)
	if err := os.MkdirAll(o.worktrees, 0o755); err != nil {
		return "", "", err
	}
	// Best-effort cleanup of any prior worktree at this path.
	_, _ = git(ctx, o.repo, "worktree", "remove", "--force", worktreePath)
	_ = os.RemoveAll(worktreePath)
	if err := removeBranchIfExists(ctx, o.repo, o.branch); err != nil {
		return "", "", err
	}
	if _, err := git(ctx, o.repo, "worktree", "add", "-b", o.branch, worktreePath, o.baseRef); err != nil {
		return "", "", err
	}
	return baseSha, worktreePath, nil
}

func cleanupWorktree(ctx context.Context, repo, worktreePath string) {
	if _, err := git(ctx, repo, "worktree", "remove", "--force", worktreePath); err != nil {
		_ = os.RemoveAll(worktreePath)
	}
}

func countCommits(ctx context.Context, repo, base string) (int, error) {
	out, err := git(ctx, repo, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

func isWorktreeOnBranch(ctx context.Context, worktreePath, branch string) bool {
	st, err := os.Stat(worktreePath)
	if err != nil || !st.IsDir() {
		return false
	}
	head, err := git(ctx, worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return false
	}
	return strings.TrimSpace(head) == branch
}

func rescueDirtyTree(ctx context.Context, repo string) (bool, error) {
	out, err := git(ctx, repo, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(out) == "" {
		return false, nil
	}
	if _, err := git(ctx, repo, "add", "-A"); err != nil {
		return false, err
	}
	if _, err := git(ctx, repo, "commit", "-m", "chore(rescue): pending changes from interrupted round"); err != nil {
		return false, err
	}
	return true, nil
}

// autoRescueCommit commits any uncommitted changes the worker left behind so
// the Judge can see them. Honours `scope` if set: only files under those
// patterns are staged, and we bail if nothing in scope is dirty.
func autoRescueCommit(ctx context.Context, repo string, round int, scope []string) (bool, error) {
	out, err := git(ctx, repo, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(out) == "" {
		return false, nil
	}
	if len(scope) > 0 {
		args := append([]string{"add", "--"}, scope...)
		if _, err := git(ctx, repo, args...); err != nil {
			return false, err
		}
	} else {
		if _, err := git(ctx, repo, "add", "-A"); err != nil {
			return false, err
		}
	}
	staged, err := git(ctx, repo, "diff", "--cached", "--name-only")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(staged) == "" {
		return false, nil
	}
	msg := fmt.Sprintf("chore(rescue): auto-commit pending worker changes (round %d)", round)
	if _, err := git(ctx, repo, "commit", "-m", msg); err != nil {
		return false, err
	}
	return true, nil
}

// errBranchMissing is returned by ensureRepo when the configured baseRef
// does not resolve.
var errBranchMissing = errors.New("base ref does not exist")

func ensureRepo(ctx context.Context, repo, baseRef string) error {
	if _, err := git(ctx, repo, "rev-parse", baseRef); err != nil {
		return fmt.Errorf("%w: %v", errBranchMissing, err)
	}
	return nil
}
