#!/usr/bin/env bash
#
# Regression test for guard-data-access.sh (ut-docs#317): proves the guard
# actually flags SAVEPOINT / ROLLBACK TO / RELEASE / BEGIN / COMMIT statement
# text planted outside internal/data and internal/db, the same way it already
# flags SELECT/INSERT/UPDATE/DELETE/CREATE TABLE — including the multi-line
# raw-string form (BEGIN;/COMMIT; as the first token on their own line), and
# proves it does NOT false-positive on ordinary identifiers that merely start
# with one of the five new keywords (RELEASE_CANDIDATE, COMMITMENT_LEVEL,
# BEGINNER_MODE) — both real bugs an earlier version of this regex had
# (independent review round 2, ut-docs#317). Plants disposable fixture files
# under internal/pages (always cleaned up via trap, even on failure), runs the
# real guard script against them, and asserts pass/fail.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-data-access.sh"
FIXTURE_DIR="internal/pages"
FAIL_COUNT=0

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
  local name="$1" body="$2"
  local path="${FIXTURE_DIR}/zz_guard_test_${name}.go"
  fixtures+=("${path}")
  cat >"${path}" <<EOF
package pages

func zzGuardTest${name}() {
	_ = \`${body}\`
}
EOF
}

# Writes a fixture verbatim (for cases plant()'s fixed one-liner shape can't
# express: a multi-line raw-string block, or several statements in one file).
plant_raw() {
  local name="$1" content="$2"
  local path="${FIXTURE_DIR}/zz_guard_test_${name}.go"
  fixtures+=("${path}")
  printf '%s\n' "${content}" >"${path}"
}

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

# Each planted statement mirrors real usage already in internal/data/pos_repo.go
# (RecordStockMovementSavepoint) — same statement shape, wrong location.
for case in \
  "Savepoint:SAVEPOINT record_stock_movement" \
  "RollbackTo:ROLLBACK TO SAVEPOINT record_stock_movement" \
  "Release:RELEASE SAVEPOINT record_stock_movement" \
  "Begin:BEGIN" \
  "Commit:COMMIT"
do
  name="${case%%:*}"
  stmt="${case#*:}"
  plant "${name}" "${stmt}"
  expect_fail "${stmt}"
  rm -f "${FIXTURE_DIR}/zz_guard_test_${name}.go"
  fixtures=()
done

# Multi-line raw-string form: BEGIN;/COMMIT; as the first token on their own
# line (the line_start path, not same_line) — an earlier version of the fix
# missed this because it required a space/EOL right after the keyword, and
# ';' is the standard terminator for a bare BEGIN/COMMIT statement.
plant_raw "MultilineBeginCommit" 'package pages

func zzGuardTestMultilineBeginCommit() {
	q := `
BEGIN;
COMMIT;
`
	_ = q
}'
expect_fail "multi-line BEGIN;/COMMIT; block"
rm -f "${FIXTURE_DIR}/zz_guard_test_MultilineBeginCommit.go"
fixtures=()

# False-positive guard: ordinary identifiers that merely start with one of
# the five new keywords must NOT trip the guard — an earlier version of the
# fix had no word-boundary check and flagged these as "inline SQL".
plant_raw "NonSqlIdentifiers" 'package pages

const buildTag = "RELEASE_CANDIDATE"

func zzGuardTestNonSqlIdentifiers() string {
	commitment := "COMMITMENT_LEVEL_HIGH"
	mode := "BEGINNER_MODE"
	return commitment + mode + buildTag
}'
expect_pass "non-SQL identifiers starting with RELEASE/COMMIT/BEGIN"
rm -f "${FIXTURE_DIR}/zz_guard_test_NonSqlIdentifiers.go"
fixtures=()

# Baseline: the guard must still pass on the real, unmodified codebase.
if ! bash "${GUARD}" >/tmp/guard_test_out.$$ 2>&1; then
  echo "❌ FAIL: guard rejects the clean codebase (false positive introduced)" >&2
  cat /tmp/guard_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ guard still passes on the clean codebase"
fi
rm -f /tmp/guard_test_out.$$

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-data-access_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-data-access_test.sh: all cases passed"
exit 0
