#!/usr/bin/env bash
#
# Regression test for guard-migration-version-collision.sh (ut-docs#1056):
# proves the guard actually flags two migration files sharing the same
# leading version number (including a zero-padding variant), flags a
# filename with no parseable version, stays quiet on ordinary unique
# migrations, and still passes on the real, unmodified codebase.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-migration-version-collision.sh"
FAIL_COUNT=0

# Scratch fixture dir, not the real internal/db/migrations (independent
# review, ut-docs#1056): that dir is //go:embed'd, and a leftover fixture
# there — trap missed on SIGKILL/power loss — would permanently skip every
# real migration above it on any install built from that tree (see the
# guard script's own comment on the high-watermark applier). A mktemp -d
# means a stray leftover is inert either way.
FIXTURE_DIR="$(mktemp -d)"
cleanup() {
  local status=$?
  rm -rf "${FIXTURE_DIR}"
  exit "${status}"
}
trap cleanup EXIT

run_guard() {
  MIGRATIONS_DIR="${FIXTURE_DIR}" bash "${GUARD}"
}

plant() {
  local name="$1"
  printf -- '-- guard test fixture\n' >"${FIXTURE_DIR}/${name}"
}

clear_fixtures() {
  rm -f "${FIXTURE_DIR}"/*.sql
}

expect_fail() {
  local label="$1"
  if run_guard >/tmp/guard_migver_test_out.$$ 2>&1; then
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
  if run_guard >/tmp/guard_migver_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_migver_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_migver_test_out.$$
}

# Two files sharing version 900 — the exact ut-docs#1056 shape — must fail.
plant "900_zz_guard_test_a.sql"
plant "900_zz_guard_test_b.sql"
expect_fail "two migrations sharing version 900"
clear_fixtures

# Same collision, but zero-padded differently ("67" vs "067") — a plain
# string compare misses this; internal/db/migration.go's strconv.Atoi
# parses both the same way, so the guard must too (independent review,
# ut-docs#1056).
plant "067_zz_guard_test_zeropad_a.sql"
plant "67_zz_guard_test_zeropad_b.sql"
expect_fail "two migrations sharing version 67 with different zero-padding"
clear_fixtures

# A filename with no parseable leading version number must fail loud, the
# same way internal/db/migration.go's loadMigrations hard-fails on it.
plant "not_a_version.sql"
expect_fail "a filename with no parseable leading version number"
clear_fixtures

# A single new unique version must not trip the guard.
plant "901_zz_guard_test_unique.sql"
expect_pass "one new unique migration version (901)"
clear_fixtures

# An empty migrations dir must not trip the guard.
expect_pass "an empty migrations directory"

# A missing/renamed migrations dir must fail loud, not pass vacuously.
rmdir "${FIXTURE_DIR}"
if MIGRATIONS_DIR="${FIXTURE_DIR}" bash "${GUARD}" >/tmp/guard_migver_test_out.$$ 2>&1; then
  echo "❌ FAIL: expected guard to reject a missing migrations directory, but it passed" >&2
  cat /tmp/guard_migver_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ guard correctly rejected a missing migrations directory"
fi
rm -f /tmp/guard_migver_test_out.$$
mkdir -p "${FIXTURE_DIR}"

# Baseline: the guard must still pass on the real, unmodified codebase
# (default MIGRATIONS_DIR, no override).
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
