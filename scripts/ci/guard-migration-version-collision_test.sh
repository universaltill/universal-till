#!/usr/bin/env bash
#
# Regression test for guard-migration-version-collision.sh (ut-docs#1056):
# proves the guard actually flags two migration files sharing the same
# leading version number, proves it stays quiet on ordinary unique
# migrations, and proves it still passes on the real, unmodified codebase.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-migration-version-collision.sh"
FIXTURE_DIR="internal/db/migrations"
FAIL_COUNT=0

# 900/901 are far above the real highest migration (069 as of ut-docs#1056)
# so these fixtures can never collide with a genuine migration number.
fixtures=()
cleanup() {
  local status=$?
  if [[ ${#fixtures[@]} -gt 0 ]]; then
    for f in "${fixtures[@]}"; do
      [[ -n "${f}" && -f "${f}" ]] && rm -f "${f}"
    done
  fi
  exit "${status}"
}
trap cleanup EXIT

plant() {
  local name="$1"
  local path="${FIXTURE_DIR}/${name}"
  fixtures+=("${path}")
  printf -- '-- guard test fixture, removed by the test script\n' >"${path}"
}

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_migver_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_migver_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_migver_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_migver_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_migver_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_migver_test_out.$$
}

clear_fixtures() {
  for f in "${fixtures[@]}"; do
    rm -f "${f}"
  done
  fixtures=()
}

# Two files sharing version 900 — the exact ut-docs#1056 shape — must fail.
plant "900_zz_guard_test_a.sql"
plant "900_zz_guard_test_b.sql"
expect_fail "two migrations sharing version 900"
clear_fixtures

# A single new unique version must not trip the guard.
plant "901_zz_guard_test_unique.sql"
expect_pass "one new unique migration version (901)"
clear_fixtures

# Baseline: the guard must still pass on the real, unmodified codebase.
if ! bash "${GUARD}" >/tmp/guard_migver_test_out.$$ 2>&1; then
  echo "❌ FAIL: guard rejects the clean codebase (false positive introduced, or a real collision exists)" >&2
  cat /tmp/guard_migver_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ guard still passes on the clean codebase"
fi
rm -f /tmp/guard_migver_test_out.$$

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-migration-version-collision_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-migration-version-collision_test.sh: all cases passed"
exit 0
