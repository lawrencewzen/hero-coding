#!/usr/bin/env bash
# Seed examples/target-repo as a fresh git repo with the intentional-bug baseline.
# Idempotent: nukes any existing .git and re-inits.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO="$ROOT/examples/target-repo"

if [ ! -d "$REPO" ]; then
  echo "missing $REPO" >&2
  exit 1
fi

cd "$REPO"
rm -rf .git
git init -q
git config user.email "hero@local"
git config user.name "hero"
git add -A
git commit -q -m "chore: seed target-repo with intentional bugs"
echo "target-repo seeded at: $REPO"
git log --oneline
