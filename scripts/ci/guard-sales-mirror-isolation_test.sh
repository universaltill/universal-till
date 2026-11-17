#!/usr/bin/env bash
#
# Regression test for guard-sales-mirror-isolation.sh (ut-docs#5074): it
# flags a query against a copy table planted outside SalesMirrorRepo, also
# for a change-log table and a child table, ignores prose in a comment and
# a same-line "sales-mirror:allow" exception, ignores test files, and passes
# on the real, unmodified tree.
set -euo pipefail

# Fixtures are planted in a throwaway copy of what the guard reads, never
# the real tree (ut-docs#4311).
# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox internal

GUARD="scripts/ci/guard-sales-mirror-isolation.sh"
FIXTURE_DIR="internal/pages"
FAIL_COUNT=0
OUT="$(mktemp)"

fixtures=()
# Invoked indirectly via `trap ... EXIT`, not a direct call -- shellcheck cannot see that (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() {
  local status=$?
  if [[ ${#fixtures[@]} -gt 0 ]]; then
    for f in "${fixtures[@]}"; do
      [[ -n "${f}" && -f "${f}" ]] && rm -f "${f}"
    done
  fi
  rm -f "${OUT}"
  remove_guard_test_sandbox
  exit "${status}"
}
trap cleanup EXIT

plant() {
  local name="$1" content="$2"
  local path="${FIXTURE_DIR}/zz_guard_test_${name}"
  fixtures+=("${path}")
  printf '%s\n' "${content}" >"${path}"
}

unplant() {
  local f
  for f in "${fixtures[@]}"; do rm -f "${f}"; done
  fixtures=()
}

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >"${OUT}" 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat "${OUT}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >"${OUT}" 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to accept ${label}, but it rejected it" >&2
    cat "${OUT}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

expect_pass "the real, unmodified tree"

plant report.go 'package pages
const q = "SELECT SUM(total) FROM mirror_sales WHERE local_date = ?"'
expect_fail "a report query against mirror_sales"
unplant

plant child.go 'package pages
const q = "SELECT * FROM mirror_sale_lines"'
expect_fail "a query against a copy child table"
unplant

plant log.go 'package pages
const q = "DELETE FROM sales_mirror_log"'
expect_fail "a write to the change log"
unplant

plant upper.go 'package pages
const q = "SELECT SUM(total) FROM MIRROR_SALES"'
expect_fail "an upper-case table name (SQLite identifiers ignore case)"
unplant

plant seed.sql 'INSERT INTO mirror_closes (kind) VALUES (1);'
expect_fail "a .sql file outside the migrations"
unplant

plant prose.go 'package pages
// mirror_sales is the copy; never read it here.
const x = 1'
expect_pass "a full-line comment naming the table"
unplant

plant allow.go 'package pages
var classified = map[string]string{"mirror_sales": "copy"} // sales-mirror:allow table classification'
expect_pass "a reviewed sales-mirror:allow exception"
unplant

plant mirror_x_test.go 'package pages
const q = "SELECT * FROM mirror_sales"'
expect_pass "a test file"
unplant

plant lookalike.go 'package pages
const q = "SELECT * FROM mirror_salesman"'
expect_pass "an identifier that only starts like a copy table"
unplant

if [[ ${FAIL_COUNT} -gt 0 ]]; then
  echo "❌ ${FAIL_COUNT} sales-mirror isolation guard case(s) failed" >&2
  exit 1
fi
echo "✓ sales-mirror isolation guard regression test passed"
