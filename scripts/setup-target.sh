#!/usr/bin/env bash
# Reset examples/target-repo to its pristine buggy baseline:
#   1. rsync from examples/target-repo-pristine/ (true source of truth)
#   2. nuke .git, re-init, single seed commit
# Idempotent. Run before every demo run to guarantee a clean baseline.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PRISTINE="$ROOT/examples/target-repo-pristine"
REPO="$ROOT/examples/target-repo"

# Resetting target-repo invalidates any in-flight hero state that points
# at the old commit graph; wipe it so the next ./hero run starts clean
# instead of trying to resume against vanished SHAs.
rm -rf "$ROOT/runs/state" "$ROOT/runs/traces" "$ROOT/worktrees"
echo "[setup-target] wiped hero runtime state (runs/state, runs/traces, worktrees)"

if [ ! -d "$PRISTINE" ]; then
  echo "missing pristine snapshot: $PRISTINE" >&2
  exit 1
fi

# Wipe and rsync. --delete drops anything the worker added; --exclude keeps node_modules.
rm -rf "$REPO"
mkdir -p "$REPO"
rsync -a --delete --exclude=node_modules --exclude=.git "$PRISTINE/" "$REPO/"

cd "$REPO"
git init -q
git config user.email "hero@local"
git config user.name "hero"
git add -A
git commit -q -m "chore: seed target-repo with intentional bugs"
echo "target-repo reset to pristine baseline at: $REPO"
git log --oneline
