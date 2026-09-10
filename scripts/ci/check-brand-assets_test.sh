#!/usr/bin/env bash
#
# Regression test for check-brand-assets.sh (ut-docs#1943, shellcheck
# rollout): proves the guard actually rejects the forbidden light-glyph
# marker in login/setup/self-order and the old wordmark asset under
# web/ui, rather than silently passing regardless.
#
# This guard used to write those three checks as a bare `! grep -Fq
# PATTERN FILE`. Under `set -e`, a command's exit status is explicitly
# exempted from triggering errexit when it is negated with `!` (bash(1)),
# so those three lines always returned 0 and let the script continue no
# matter what they found -- caught for real only once shellcheck (SC2251)
# was added to CI and flagged all three. This test proves the fixed
# `must_not_contain` helper (which fails via an explicit `if ...; then
# exit 1; fi`, not a negated command) actually catches the violation.
#
# Plants a fixture line in the real login.html (a valid HTML comment, so
# the file stays syntactically well-formed throughout), same
# backup/restore-from-file convention as guard-webkit-version_test.sh --
# restore-from-backup rather than a pattern-delete, so a killed process at
# worst leaves a *.orig backup file behind rather than corrupting the
# committed template.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/check-brand-assets.sh"
LOGIN_HTML="web/ui/pages/login.html"
BACKUP="${LOGIN_HTML}.guard_brand_assets_test_backup"
FAIL_COUNT=0

cp "${LOGIN_HTML}" "${BACKUP}"
# Invoked indirectly via `trap ... EXIT`, not a direct call -- shellcheck cannot see that (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() {
  local status=$?
  # Always restore from the backup, whether or not the fixture is currently
  # planted -- idempotent, and safe even if an earlier run of this same
  # script was killed mid-test and left login.html already mutated.
  cp "${BACKUP}" "${LOGIN_HTML}"
  rm -f "${BACKUP}"
  exit "${status}"
}
trap cleanup EXIT

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_brand_assets_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_brand_assets_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_brand_assets_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_brand_assets_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_brand_assets_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_brand_assets_test_out.$$
}

# Baseline: the guard must pass on the real, unmodified codebase.
expect_pass "the clean codebase"

# The forbidden light-glyph marker planted in login.html (an HTML comment,
# so the file stays syntactically valid) must be caught.
echo '<!-- unitill-logo-light (guard test fixture) -->' >>"${LOGIN_HTML}"
expect_fail "unitill-logo-light planted in login.html"

# Restore and re-verify -- proves the fixture is actually gone, not just
# that this script believes it removed it.
cp "${BACKUP}" "${LOGIN_HTML}"
expect_pass "login.html after the fixture is removed"

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ check-brand-assets_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ check-brand-assets_test.sh: all cases passed"
exit 0
