#!/usr/bin/env bash
#
# Self-test for guard-deadcode-baseline-growth.sh (ut-docs#3406). Builds a
# throwaway git repo with a base commit, then proves: a baseline entry added
# together with other changes fails; a baseline-only change passes; a moved
# entry (same function, new file) passes alongside other changes; burning an
# entry down passes; and a missing base ref skips with a notice.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-deadcode-baseline-growth.sh"
FAILS=0
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

REPO="${WORK}/repo"
LIST="${REPO}/scripts/ci/deadcode-baseline.txt"
mkdir -p "${REPO}/internal/x" "${REPO}/scripts/ci"
printf 'package x\nfunc A() {}\n' >"${REPO}/internal/x/x.go"
printf 'internal/x/old.go: unreachable func: Old\n' >"${LIST}"

g() { git -C "${REPO}" -c user.name=t -c user.email=t@example.invalid -c core.hooksPath=/dev/null -c commit.gpgsign=false "$@"; }
g init -q
g add -A
g commit -qm base
g tag base

run() { DEADCODE_BASELINE_BASE="$1" bash "${GUARD}" "${REPO}" >"${WORK}/out" 2>&1; }
expect() {
  local want="$1" label="$2" base="$3" got=0
  run "${base}" || got=1
  if [ "${got}" = "${want}" ]; then
    echo "✓ ${label}"
  else
    echo "❌ FAIL: ${label} (exit ${got}, want ${want})" >&2
    cat "${WORK}/out" >&2
    FAILS=$((FAILS + 1))
  fi
}
reset_to_base() { g reset -q --hard base; g clean -qfd; }

expect 0 "unchanged tree passes" base

# The #3406 shape: new dead code and its own baseline entry in one change.
printf 'package x\nfunc A() {}\nfunc DeadNew() {}\n' >"${REPO}/internal/x/x.go"
printf 'internal/x/x.go: unreachable func: DeadNew\n' >>"${LIST}"
expect 1 "new entry added alongside a code change fails (working tree)" base
if ! grep -q 'internal/x/x.go: unreachable func: DeadNew' "${WORK}/out"; then
  echo "❌ FAIL: rejection does not name the self-baselined entry" >&2
  FAILS=$((FAILS + 1))
fi
g commit -qam "dead code + self-baseline"
expect 1 "new entry added alongside a code change fails (committed)" base
reset_to_base

# Separate, baseline-only change: the reviewable escape hatch.
printf 'internal/x/x.go: unreachable func: DeadNew\n' >>"${LIST}"
g commit -qam "baseline only"
expect 0 "baseline-only change passes" base
reset_to_base

# A file rename moves an existing entry: same function, not growth.
printf 'internal/x/renamed.go: unreachable func: Old\n' >"${LIST}"
printf 'package x\nfunc A() {}\nfunc B() {}\n' >"${REPO}/internal/x/x.go"
g commit -qam "move entry"
expect 0 "moved entry (same function name) passes alongside code changes" base
reset_to_base

# A generic receiver's name (brackets) moves like any other.
printf 'internal/x/list.go: unreachable func: List[T].Old\n' >"${LIST}"
g commit -qam "generic entry"
g tag -f base >/dev/null
printf 'internal/x/list2.go: unreachable func: List[T].Old\n' >"${LIST}"
printf 'package x\nfunc A() {}\nfunc F() {}\n' >"${REPO}/internal/x/x.go"
expect 0 "moved generic-receiver entry passes" base
g reset -q --hard HEAD~1
g tag -f base >/dev/null
g clean -qfd

# Burning an entry down alongside code is always fine.
: >"${LIST}"
printf 'package x\nfunc A() {}\nfunc C() {}\n' >"${REPO}/internal/x/x.go"
g commit -qam "burn down"
expect 0 "removing an entry alongside code changes passes" base
reset_to_base

# The baseline-only PR still carries its review record (CLAUDE.md).
mkdir -p "${REPO}/docs/code-reviews"
printf '# review\n' >"${REPO}/docs/code-reviews/2026-10-09-baseline.md"
printf 'internal/x/x.go: unreachable func: DeadNew\n' >>"${LIST}"
expect 0 "baseline-only change with its review record passes" base
reset_to_base

# One removed name licenses one move, not several same-named additions.
printf 'internal/a/a.go: unreachable func: Old\ninternal/b/b.go: unreachable func: Old\n' >"${LIST}"
printf 'package x\nfunc A() {}\nfunc E() {}\n' >"${REPO}/internal/x/x.go"
expect 1 "one removed entry cannot license two same-named additions" base
reset_to_base

# Untracked new file + baseline entry still counts as a code change.
printf 'package x\nfunc D() {}\n' >"${REPO}/internal/x/d.go"
printf 'internal/x/d.go: unreachable func: D\n' >>"${LIST}"
expect 1 "new entry with an untracked new code file fails" base
reset_to_base

printf 'internal/x/x.go: unreachable func: DeadNew\n' >>"${LIST}"
printf 'package x\nfunc A() {}\nfunc DeadNew() {}\n' >"${REPO}/internal/x/x.go"
expect 0 "missing base ref skips the check" missing-ref
if ! grep -q 'base ref missing-ref not available' "${WORK}/out"; then
  echo "❌ FAIL: no skip notice without a base ref" >&2
  FAILS=$((FAILS + 1))
fi

if [ "${FAILS}" -ne 0 ]; then
  echo "❌ ${FAILS} guard-deadcode-baseline-growth case(s) failed" >&2
  exit 1
fi
echo "✓ guard-deadcode-baseline-growth_test.sh: all cases passed"
