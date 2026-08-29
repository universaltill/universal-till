#!/usr/bin/env bash
#
# Regression test for guard-webkit-version.sh (ut-docs#1286): proves the
# guard actually flags a stray webkit2gtk-4.0 reference planted in
# .github/workflows/ci.yml, not just release.yml/.goreleaser.yaml. Found by
# independent review of universal-till#624 — ci.yml gained its own
# libwebkit2gtk apt line and the guard couldn't see it. Plants a disposable
# comment line in the real ci.yml, runs the real guard script, asserts
# pass/fail, then restores ci.yml from a byte-for-byte backup — restore-from-
# backup rather than a pattern-delete, so a killed process (SIGKILL, OOM) at
# worst leaves a *.orig backup file behind rather than corrupting the
# committed workflow (independent review, round 1): the trap and the
# explicit restore both just copy the backup back over ci.yml, so a partial
# or wrong pattern match can never eat an unrelated line.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-webkit-version.sh"
CI_YML=".github/workflows/ci.yml"
BACKUP="${CI_YML}.guard_webkit_test_backup"
FAIL_COUNT=0

cp "${CI_YML}" "${BACKUP}"
cleanup() {
  local status=$?
  # Always restore from the backup, whether or not the fixture is currently
  # planted — idempotent, and safe to run even if an earlier run of this
  # same script was killed mid-test and left ci.yml already mutated.
  cp "${BACKUP}" "${CI_YML}"
  rm -f "${BACKUP}"
  exit "${status}"
}
trap cleanup EXIT

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_test_out.$$
}

# Baseline: the guard must pass on the real, unmodified codebase.
expect_pass "the clean codebase"

# A stray webkit2gtk-4.0 reference planted in ci.yml (a YAML comment, so the
# file stays syntactically valid throughout) must be caught the same way one
# in release.yml already is.
echo "      # webkit2gtk-4.0 (guard test fixture)" >>"${CI_YML}"
expect_fail "webkit2gtk-4.0 planted in ci.yml"

# Restore and re-verify — proves the fixture is actually gone, not just that
# this script believes it removed it (independent review, round 1: an earlier
# sed-based delete had no such re-check, so a drifted pattern could silently
# leave the violation in place while still reporting success).
cp "${BACKUP}" "${CI_YML}"
expect_pass "ci.yml after the fixture is removed"

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-webkit-version_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-webkit-version_test.sh: all cases passed"
exit 0
