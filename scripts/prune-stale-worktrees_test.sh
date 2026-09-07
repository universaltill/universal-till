#!/usr/bin/env bash
#
# Regression test for prune-stale-worktrees.sh (ut-docs#1567): proves the
# script only ever removes a worktree that is (a) not locked, (b) fully
# merged into origin/main, (c) clean (no uncommitted changes) and (d) past
# the age cutoff — and leaves everything else alone, each reason checked
# independently and, for the unmerged/dirty cases, deliberately made ALSO
# old (backdated the same way as the removed case) so the test actually
# exercises the merge/dirty check itself rather than incidentally passing
# via the separate age check (independent review, ut-docs#1567 round 1: an
# earlier version of the "unmerged" case was accidentally young too, so
# disabling the merge-base check entirely still left the whole suite
# green):
#   - merged + clean + old            -> removed
#   - unmerged (unique commit) + old  -> kept, even past the age cutoff
#   - merged + dirty (uncommitted)+old-> kept, even past the age cutoff
#   - merged + clean + young          -> kept
#   - merged + clean + old + locked   -> kept, lock alone is enough
#
# Runs entirely inside a disposable scratch git repo + bare "origin" under
# mktemp -d (always cleaned up via trap, even on failure) — never touches
# this repo's own real .claude/worktrees/ or makes a network call.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/prune-stale-worktrees.sh"
FAIL_COUNT=0

SCRATCH="$(mktemp -d)"
cleanup() {
  local status=$?
  cd /
  rm -rf "${SCRATCH}"
  exit "${status}"
}
trap cleanup EXIT

ORIGIN="${SCRATCH}/origin.git"
WORK="${SCRATCH}/work"

git init --quiet --bare "${ORIGIN}"
git init --quiet -b main "${WORK}"
cd "${WORK}"
git config user.email "test@example.com"
git config user.name "Test"
git remote add origin "${ORIGIN}"

echo "seed" >README.md
git add README.md
git commit --quiet -m "initial"
git push --quiet origin main

mkdir -p scripts .claude/worktrees
cp "${SCRIPT}" scripts/prune-stale-worktrees.sh
chmod +x scripts/prune-stale-worktrees.sh

# Each worktree below is its OWN branch point off the original "initial"
# commit (not chained off each other's commits) so one worktree's commit
# date can never leak into another's via a shared advancing `main` tip.
old_epoch=$(( $(date +%s) - (10 * 86400) ))

make_merged_worktree() {
  local dir="$1" commit_epoch="$2"
  git worktree add --detach --quiet ".claude/worktrees/${dir}" main
  (
    cd ".claude/worktrees/${dir}"
    GIT_AUTHOR_DATE="@${commit_epoch}" GIT_COMMITTER_DATE="@${commit_epoch}" \
      git commit --quiet --allow-empty -m "content dated ${commit_epoch}, folded into main"
  )
  # Fold that commit into main so it really is an ancestor, without moving
  # the worktree's own HEAD off of it — each fold is its own merge commit
  # (not a fast-forward) so later folds can't change an earlier worktree's
  # HEAD commit or its date.
  local sha
  sha="$(git -C ".claude/worktrees/${dir}" rev-parse HEAD)"
  git merge --quiet --no-ff -m "merge ${dir}" "${sha}"
  git push --quiet origin main
}

make_merged_worktree "merged-old" "${old_epoch}"
make_merged_worktree "merged-old-dirty" "${old_epoch}"
echo "uncommitted" >".claude/worktrees/merged-old-dirty/scratch.txt"

make_merged_worktree "merged-old-locked" "${old_epoch}"
git worktree lock ".claude/worktrees/merged-old-locked" --reason "in-use by test"

# Old AND unmerged — the commit is never folded into main, so this proves
# the merge-base check alone protects it, independent of age.
git worktree add --detach --quiet ".claude/worktrees/unmerged" main
(
  cd ".claude/worktrees/unmerged"
  GIT_AUTHOR_DATE="@${old_epoch}" GIT_COMMITTER_DATE="@${old_epoch}" \
    git commit --quiet --allow-empty -m "unique work, never merged, also old"
)

git worktree add --detach --quiet ".claude/worktrees/merged-young" main

run_prune() {
  bash scripts/prune-stale-worktrees.sh 3 main
}

expect_removed() {
  local dir="$1"
  if [[ -d ".claude/worktrees/${dir}" ]]; then
    echo "❌ FAIL: expected ${dir} to be removed, but it still exists" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ ${dir} correctly removed"
  fi
}

expect_kept() {
  local dir="$1" reason="$2"
  if [[ -d ".claude/worktrees/${dir}" ]]; then
    echo "✓ ${dir} correctly kept (${reason})"
  else
    echo "❌ FAIL: expected ${dir} to be kept (${reason}), but it was removed" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

OUT="$(run_prune)"
echo "${OUT}"

expect_removed "merged-old"
expect_kept "merged-old-dirty" "uncommitted changes"
expect_kept "merged-old-locked" "worktree is locked"
expect_kept "unmerged" "holds a commit not on origin/main, despite also being old"
expect_kept "merged-young" "younger than the retention window"

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ prune-stale-worktrees_test.sh: ${FAIL_COUNT} failure(s)" >&2
  exit 1
fi
echo "✓ prune-stale-worktrees_test.sh: all cases passed"
