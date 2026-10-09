#!/usr/bin/env bash
#
# Guard (ut-docs#3406): scripts/ci/deadcode-baseline.txt may not grow in the
# same change that touches anything else. guard-deadcode-baseline.sh fails a
# PR that adds an unreachable function, but a PR could silence it by adding
# that function to the baseline itself — a self-granted CI bypass. A new
# entry must arrive in its own baseline-only PR, so the exception is
# reviewed on its own rather than buried in a feature diff. Otherwise:
# delete the dead code, or land it with its first caller.
#
# Allowed alongside other changes: removing entries (burn-down, ut-docs#1566)
# and moving one (an added entry whose function name matches a removed one —
# a file rename or a function moved between files; each removed entry
# licenses one move). "Baseline-only" still lets the PR carry its own review
# record (docs/code-reviews/*.md), which every change needs (CLAUDE.md).
#
# Env: DEADCODE_BASELINE_BASE (default origin/main) — the ref to compare
# against; the working tree (tracked changes + untracked files) is compared
# to it, so it works on a CI merge checkout and locally alike. Unresolvable
# ref (a local clone without it) → skipped with a notice; CI fetches it.
#
# Args (fixture testing): [repo root].
set -euo pipefail
export LC_ALL=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCAN_ROOT="${1:-${ROOT_DIR}}"
BASE_REF="${DEADCODE_BASELINE_BASE:-origin/main}"
LIST_PATH="scripts/ci/deadcode-baseline.txt"

if ! git -C "${SCAN_ROOT}" rev-parse --verify --quiet "${BASE_REF}^{commit}" >/dev/null; then
  echo "ℹ deadcode-baseline growth guard: base ref ${BASE_REF} not available — check skipped (CI fetches it)"
  exit 0
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# A base without the file (the PR that introduces it) counts as empty.
git -C "${SCAN_ROOT}" show "${BASE_REF}:${LIST_PATH}" 2>/dev/null | tr -d '\r' | sort -u >"${WORK}/base.txt" || true
if [[ -f "${SCAN_ROOT}/${LIST_PATH}" ]]; then
  tr -d '\r' <"${SCAN_ROOT}/${LIST_PATH}" | sort -u >"${WORK}/current.txt"
else
  : >"${WORK}/current.txt"
fi

comm -13 "${WORK}/base.txt" "${WORK}/current.txt" | sed '/^$/d' >"${WORK}/added.txt"
comm -23 "${WORK}/base.txt" "${WORK}/current.txt" | sed '/^$/d' >"${WORK}/removed.txt"

# Function names ("unreachable func: Name") of removed entries: an added
# entry with one of these names is a move, not growth. Each removed entry
# licenses one move (a multiset), so one stale name can't cover several.
declare -A movable=()
while IFS= read -r fn; do
  [[ -n "${fn}" ]] && movable["${fn}"]=$(( ${movable["${fn}"]:-0} + 1 ))
done < <(sed -nE 's/^.*: unreachable func: (.*)$/\1/p' "${WORK}/removed.txt")
growth=()
while IFS= read -r entry; do
  fn="$(sed -nE 's/^.*: unreachable func: (.*)$/\1/p' <<<"${entry}")"
  if [[ -n "${fn}" ]] && [[ "${movable["${fn}"]:-0}" -gt 0 ]]; then
    movable["${fn}"]=$(( ${movable["${fn}"]} - 1 ))
    continue
  fi
  growth+=("${entry}")
done <"${WORK}/added.txt"

if [[ "${#growth[@]}" -eq 0 ]]; then
  echo "✓ deadcode-baseline growth guard: ${LIST_PATH} gains no new entries against ${BASE_REF}"
  exit 0
fi

{
  git -C "${SCAN_ROOT}" diff --name-only "${BASE_REF}" --
  git -C "${SCAN_ROOT}" ls-files --others --exclude-standard
} | sort -u | { grep -vxF -- "${LIST_PATH}" || true; } \
  | { grep -vE '^docs/code-reviews/[^/]+\.md$' || true; } >"${WORK}/other_changes.txt"

if [[ ! -s "${WORK}/other_changes.txt" ]]; then
  echo "✓ deadcode-baseline growth guard: baseline-only change adds ${#growth[@]} entry/entries — review each on its own:"
  printf '  %s\n' "${growth[@]}"
  exit 0
fi

echo "❌ deadcode-baseline growth guard (ut-docs#3406): ${LIST_PATH} gains entries in a change that also touches other files:" >&2
printf '  %s\n' "${growth[@]}" >&2
echo "" >&2
echo "A change may not baseline its own dead code. Remove the function, or land" >&2
echo "it together with its first caller. A genuine exception (e.g. reachable only" >&2
echo "from a build tag the analysis can't see) goes in a separate PR that changes" >&2
echo "only ${LIST_PATH} (plus its docs/code-reviews/ record), with the reason" >&2
echo "in its description." >&2
echo "Other files changed against ${BASE_REF} (first 10):" >&2
head -n 10 "${WORK}/other_changes.txt" | sed 's/^/  /' >&2
exit 1
