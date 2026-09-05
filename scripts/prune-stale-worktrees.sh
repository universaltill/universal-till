#!/usr/bin/env bash
#
# Removes stale agent-created git worktrees under .claude/worktrees/ (ut-docs#1567).
#
# Isolated-worktree agent runs (Agent(isolation: "worktree") / EnterWorktree
# with a `name` — see ut-docs/.claude/skills/scrum-master/STANDING-CONTEXT.md)
# create real registered git worktrees there. Nothing currently removes them
# when a run ends, so they accumulate: 4 stale detached-HEAD worktrees were
# found holding 233 MB on 2026-09-04, and every repo-wide grep matched each
# hit 5 times over — once for real, four times from stale copies at old
# commits. That is the same class of hazard as the stale-checkout rule in
# the ecosystem CLAUDE.md: reasoning confidently about code that isn't there
# (or, here, duplicated code that no longer matches `main`).
#
# A worktree is only ever auto-removed when ALL of these hold:
#   - it is not locked (`git worktree lock`) — a lock means something is
#     actively using it (e.g. a running agent session) regardless of what
#     its content looks like; never even consider it for removal, AND
#   - its HEAD commit is an ancestor of origin/<default branch> (nothing
#     unique — the same "never discard a commit that isn't upstream yet"
#     safety the cycle-start sync guard in SKILL.md already uses), AND
#   - it has no uncommitted changes (`git status --porcelain` is empty), AND
#   - it is older than the retention window (commit time, default 3 days).
# Anything else is left alone and reported — this script only ever deletes
# work that is both fully upstream and stale, never unmerged, fresh or
# in-use work. If `git worktree remove --force` itself refuses for any
# other reason, this script reports it and moves on rather than falling
# back to a raw `rm -rf` — a git-level refusal on a worktree that already
# passed all three checks above is a signal to investigate, not to brute
# force past (independent review, ut-docs#1567: an earlier version's
# `rm -rf` fallback defeated git's own lock protection outright).
#
# Usage: scripts/prune-stale-worktrees.sh [max_age_days] [default_branch]
#   max_age_days    default 3
#   default_branch  default: origin's HEAD branch, else "main"
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

MAX_AGE_DAYS="${1:-3}"
DEFAULT_BRANCH="${2:-}"
WORKTREES_DIR="${ROOT_DIR}/.claude/worktrees"

if [[ -z "${DEFAULT_BRANCH}" ]]; then
  DEFAULT_BRANCH="$(git remote show origin 2>/dev/null | awk '/HEAD branch/ {print $NF}')"
  DEFAULT_BRANCH="${DEFAULT_BRANCH:-main}"
fi

if [[ ! -d "${WORKTREES_DIR}" ]]; then
  echo "prune-stale-worktrees: no .claude/worktrees/ directory — nothing to prune"
  exit 0
fi

# Relies on the default clone's fetch refspec also updating
# refs/remotes/origin/<branch> — true for a normal full clone, not
# guaranteed under a shallow/single-branch checkout. Fails safe either
# way: merge-base then errors, which prune_one treats as "unmerged" and
# keeps — worst case is under-pruning, never data loss.
git fetch origin "${DEFAULT_BRANCH}" --quiet 2>/dev/null || true

now_epoch="$(date +%s)"
max_age_seconds=$(( MAX_AGE_DAYS * 86400 ))
removed=0
kept_unmerged=0
kept_dirty=0
kept_young=0
kept_locked=0
kept_refused=0

wt_path=""
wt_head=""
wt_locked=0

prune_one() {
  local path="$1" head="$2" locked="$3"
  [[ -z "${path}" ]] && return 0
  case "${path}" in
    "${WORKTREES_DIR}"/*) ;;
    *) return 0 ;;  # not one of ours — never touch worktrees outside .claude/worktrees/
  esac
  [[ -d "${path}" ]] || return 0

  if [[ "${locked}" == "1" ]]; then
    echo "KEEPING ${path} (${head:0:8}) — worktree is locked (actively in use)" >&2
    kept_locked=$((kept_locked + 1))
    return 0
  fi

  if ! git merge-base --is-ancestor "${head}" "origin/${DEFAULT_BRANCH}" 2>/dev/null; then
    echo "KEEPING ${path} (${head:0:8}) — holds commits not on origin/${DEFAULT_BRANCH}" >&2
    kept_unmerged=$((kept_unmerged + 1))
    return 0
  fi

  if [[ -n "$(git -C "${path}" status --porcelain 2>/dev/null)" ]]; then
    echo "KEEPING ${path} (${head:0:8}) — has uncommitted changes" >&2
    kept_dirty=$((kept_dirty + 1))
    return 0
  fi

  local commit_epoch age_seconds
  commit_epoch="$(git log -1 --format=%ct "${head}" 2>/dev/null || echo "${now_epoch}")"
  age_seconds=$(( now_epoch - commit_epoch ))
  if (( age_seconds < max_age_seconds )); then
    kept_young=$((kept_young + 1))
    return 0
  fi

  echo "removing merged, stale worktree: ${path} (${head:0:8}, $(( age_seconds / 86400 ))d old)"
  if git worktree remove --force "${path}" 2>/tmp/prune-worktree-remove-err.$$; then
    removed=$((removed + 1))
  else
    echo "KEEPING ${path} — git worktree remove refused: $(cat /tmp/prune-worktree-remove-err.$$)" >&2
    kept_refused=$((kept_refused + 1))
  fi
  rm -f /tmp/prune-worktree-remove-err.$$
}

while IFS= read -r line; do
  case "${line}" in
    "worktree "*) wt_path="${line#worktree }" ;;
    "HEAD "*) wt_head="${line#HEAD }" ;;
    "locked"*) wt_locked=1 ;;
    "")
      prune_one "${wt_path}" "${wt_head}" "${wt_locked}"
      wt_path=""
      wt_head=""
      wt_locked=0
      ;;
    *) ;;
  esac
done < <(git worktree list --porcelain; printf '\n')

git worktree prune

echo "prune-stale-worktrees: removed ${removed}, kept ${kept_unmerged} (unmerged), ${kept_dirty} (dirty), ${kept_young} (younger than ${MAX_AGE_DAYS}d), ${kept_locked} (locked), ${kept_refused} (git refused removal)"
