#!/usr/bin/env bash
#
# Regression test for guard-deadcode-baseline.sh (ut-docs#1581): proves the
# guard actually catches a NEW unreachable function planted in
# cmd/unitill-desktop, and that it accepts the clean, unmodified baseline.
# Plants a disposable fixture file (a real .go file, so the fixture is
# genuinely compiled and analyzed rather than merely textually present),
# runs the real guard, asserts pass/fail, then removes the fixture and
# re-verifies -- same restore-and-recheck shape as
# guard-webkit-version_test.sh, so a killed process at worst leaves a
# stray fixture file behind (harmless,
# cmd/unitill-desktop/zzz_guard_test_fixture.go, never committed -- a
# fresh git worktree/clone never has it) rather than corrupting real
# source.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-deadcode-baseline.sh"
FIXTURE="cmd/unitill-desktop/zzz_guard_test_fixture.go"
FAIL_COUNT=0

cleanup() {
  local status=$?
  rm -f "${FIXTURE}"
  exit "${status}"
}
trap cleanup EXIT

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_deadcode_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_deadcode_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_deadcode_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_deadcode_test_out.$$ 2>&1; then
    echo "✓ guard correctly accepted ${label}"
  else
    echo "❌ FAIL: expected guard to accept ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_deadcode_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_deadcode_test_out.$$
}

# Baseline: the guard must pass against the real, unmodified checked-in
# baseline (scripts/ci/deadcode-baseline.txt) on the real codebase.
expect_pass "the clean codebase against its checked-in baseline"

# A brand-new, genuinely-unreachable exported function, not in the
# baseline, must be caught -- this is the guard's entire reason to exist.
cat >"${FIXTURE}" <<'EOF'
package main

// GuardTestFixtureUnreachable exists only for
// scripts/ci/guard-deadcode-baseline_test.sh -- it is never called from
// anywhere, on purpose, so the deadcode-baseline guard must flag it as a
// new finding not present in the checked-in baseline.
func GuardTestFixtureUnreachable() string {
	return "unreachable"
}
EOF
expect_fail "a new unreachable function not in the baseline"

# Restore and re-verify -- proves the fixture is actually gone, not just
# that this script believes it removed it.
rm -f "${FIXTURE}"
expect_pass "the codebase after the fixture is removed"

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-deadcode-baseline_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-deadcode-baseline_test.sh: all cases passed"
exit 0
