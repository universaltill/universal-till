#!/usr/bin/env bash
#
# Regression test for guard-price-history-sync.sh (ADR-0099 Decision 3,
# ut-docs#2348; originally ut-docs#1671): proves the guard flags a SQL write
# to price_history (INSERT INTO / UPDATE / DELETE FROM) planted in a
# function that is not on its explicit file:function allowlist — in a file
# outside internal/data entirely, AND in a new file inside internal/data
# (the allowlist is per file, not per package), AND in a function that
# merely REUSES an allowlisted function's name in a different file (the
# allowlist is file:function, not function-name-only), AND as a
# package-level string with no enclosing func at all — proves it does NOT
# flag a write planted in a _test.go file or under .claude/worktrees/
# (ut-docs#2129), proves it fails closed (not open) when the classification
# file is missing, and proves it gets out of the way once price_history is
# no longer classified in nonAdminTables. Also proves the guard still passes
# on the real codebase — which is the end-to-end proof that every real
# writer, including sync_admin_repo.go's own invalidation UPDATE, is
# correctly allowlisted.
#
# None of this mutates the real, tracked internal/data/sync_admin_repo.go —
# the reclassification and missing-file cases instead point the guard's
# SYNC_CLASSIFICATION_FILE override at scratch files, the same convention
# guard-migration-version-collision.sh's MIGRATIONS_DIR override uses, so
# a killed run can never leave a stray .bak or a clobbered file mode on
# real source. The planted files are plain text to the guard (it never
# compiles them) and are removed by the EXIT trap.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-price-history-sync.sh"
FAIL_COUNT=0

fixtures=()
scratch_dir=""
worktree_decoy_root=""
# Invoked indirectly via `trap ... EXIT`, not a direct call -- shellcheck cannot see that (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() {
  local status=$?
  if [[ ${#fixtures[@]} -gt 0 ]]; then
    for f in "${fixtures[@]}"; do
      [[ -n "${f}" && -f "${f}" ]] && rm -f "${f}"
    done
  fi
  if [[ -n "${scratch_dir}" && -d "${scratch_dir}" ]]; then
    rm -rf "${scratch_dir}"
  fi
  # Absolute path to this test's own uniquely-named decoy directory only —
  # never a glob, and never anywhere near a real .claude/worktrees/agent-*
  # tree a live Agent(isolation: "worktree") run may have checked out.
  if [[ -n "${worktree_decoy_root}" && -d "${worktree_decoy_root}" ]]; then
    rm -rf "${worktree_decoy_root}"
  fi
  exit "${status}"
}
trap cleanup EXIT

scratch_dir="$(mktemp -d)"

plant() {
  local dir="$1" pkg="$2" name="$3" content="$4"
  local path="${dir}/zz_guard_test_${name}.go"
  fixtures+=("${path}")
  printf 'package %s\n\n%s\n' "${pkg}" "${content}" >"${path}"
}

clear_fixtures() {
  if [[ ${#fixtures[@]} -gt 0 ]]; then
    for f in "${fixtures[@]}"; do
      rm -f "${f}"
    done
  fi
  fixtures=()
}

run_guard() {
  # Any extra args are env assignments the guard should see (e.g. a
  # SYNC_CLASSIFICATION_FILE override) — passed through explicitly rather
  # than exported, so a case that doesn't need one can't leak into the next.
  env "$@" bash "${GUARD}"
}

expect_fail() {
  local label="$1"
  shift
  if run_guard "$@" >/tmp/guard_price_history_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_price_history_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif ! grep -q '❌ price-history-sync guard:' /tmp/guard_price_history_test_out.$$; then
    # A non-zero exit alone is not a rejection — a bash syntax error or a
    # crashed awk/sed pipeline also exits non-zero. Require the guard's own
    # rejection banner so a broken guard can't pass its own test.
    echo "❌ FAIL: guard exited non-zero on ${label}, but without its own rejection message (crashed, not rejected?)" >&2
    cat /tmp/guard_price_history_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_price_history_test_out.$$
}

expect_pass() {
  local label="$1"
  shift
  if run_guard "$@" >/tmp/guard_price_history_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_price_history_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_price_history_test_out.$$
}

# A write planted outside internal/data entirely (an admin page, say)
# while price_history is still non-admin must be rejected — one case per
# statement kind the guard matches.
plant "internal/pages" "pages" "UpdateWriter" 'func zzGuardTestUpdateWriter() string {
	return `UPDATE price_history SET ends_at = NULL WHERE item_id = ?`
}'
expect_fail "an UPDATE price_history planted outside internal/data"
clear_fixtures

plant "internal/pages" "pages" "InsertWriter" 'func zzGuardTestInsertWriter() string {
	return `INSERT INTO price_history(id, item_id, price, starts_at) VALUES(?,?,?,?)`
}'
expect_fail "an INSERT INTO price_history planted outside internal/data"
clear_fixtures

plant "internal/pages" "pages" "DeleteWriter" 'func zzGuardTestDeleteWriter() string {
	return `DELETE FROM price_history WHERE item_id = ?`
}'
expect_fail "a DELETE FROM price_history planted outside internal/data"
clear_fixtures

# A write planted INSIDE internal/data, in a new file, must ALSO be
# rejected — the allowlist is per file:function, not "anything in the data
# layer is fine" (guard-data-access.sh already allows raw SQL there; this
# guard is the narrower question of WHICH functions may write this table).
plant "internal/data" "data" "NewDataWriter" 'func zzGuardTestNewDataWriter() string {
	return `UPDATE price_history SET ends_at = CURRENT_TIMESTAMP WHERE ends_at IS NULL`
}'
expect_fail "a write inside internal/data but in a function not on the allowlist"
clear_fixtures

# Reusing an allowlisted function NAME in a different file must not pass:
# the key is file:function, so a same-named function elsewhere is a new,
# unreviewed writer.
plant "internal/data" "data" "NameSquatter" 'func AppendPriceHistoryItem() string {
	return `INSERT INTO price_history(id) VALUES(?)`
}'
expect_fail "an allowlisted function NAME reused in a non-allowlisted file"
clear_fixtures

# A package-level SQL string with no enclosing func can never be allowlisted
# (the unit of review is a named function) and must fail.
plant "internal/data" "data" "PackageLevel" 'var zzGuardTestPackageLevel = `DELETE FROM price_history WHERE 1`'
expect_fail "a package-level price_history write string with no enclosing func"
clear_fixtures

# ut-docs#2129: a write planted under .claude/worktrees/ (an agent
# worktree's own copy of this repo's files at some other commit — see
# universal-till/CLAUDE.md's "Agent worktree hygiene") must NOT trip the
# guard, even though it is a real, textually-matching write in a
# non-allowlisted location — it's a copy of a writer tracked (or planted)
# elsewhere, not a new one. A dedicated, uniquely-named directory (never
# touching the real .claude/worktrees/agent-* trees a live
# Agent(isolation: "worktree") run may have checked out) so this test can
# never step on one; removed with an absolute-path `rm -rf`, never a glob,
# on this test's own directory only.
worktree_decoy_root="${ROOT_DIR}/.claude/worktrees/zz-guard-test-fixture-2129"
worktree_decoy_dir="${worktree_decoy_root}/internal/pages"
mkdir -p "${worktree_decoy_dir}"
worktree_decoy_path="${worktree_decoy_dir}/zz_guard_test_WorktreeDecoy.go"
printf 'package pages\n\nfunc zzGuardTestWorktreeDecoy() string {\n\treturn `UPDATE price_history SET ends_at = NULL`\n}\n' >"${worktree_decoy_path}"
expect_pass "a write planted under .claude/worktrees/ (agent worktree copy)"
rm -rf "${worktree_decoy_root}"
worktree_decoy_root=""

# A write planted in a _test.go file must not trip the guard (test
# fixtures INSERT into price_history all the time — mirrors the deadcode
# guard's own test-file exclusion).
path="internal/pages/zz_guard_test_TestFileWrite_test.go"
fixtures+=("${path}")
printf 'package pages\n\nimport "testing"\n\nfunc TestZzGuardTestFileWrite(t *testing.T) {\n\t_ = `INSERT INTO price_history(id, item_id, price, starts_at) VALUES(?,?,?,?)`\n}\n' >"${path}"
expect_pass "a write inside a _test.go file"
clear_fixtures

# A whole-line comment mentioning the statement is prose, not a write.
plant "internal/pages" "pages" "CommentOnly" '// This page never runs UPDATE price_history itself; see ADR-0099.
func zzGuardTestCommentOnly() {}'
expect_pass "a whole-line comment naming the statement"
clear_fixtures

# Missing classification file must fail CLOSED (loud error), not silently
# pass as if price_history had been reclassified — a plain `grep -q`
# treats "file not found" and "pattern not found" identically, so this
# needs the guard's explicit existence check, not just the grep.
expect_fail "a missing SYNC_CLASSIFICATION_FILE" \
  "SYNC_CLASSIFICATION_FILE=${scratch_dir}/does-not-exist.go"

# Once price_history is no longer classified non-admin (a deliberate
# re-classification), the guard must get out of the way even with a real
# non-allowlisted writer present — verified against a scratch
# classification file, never the real tracked one.
printf 'package data\n\nvar nonAdminTables = map[string]string{\n\t"other_table": "unrelated",\n}\n' >"${scratch_dir}/sync_admin_repo.go"
plant "internal/pages" "pages" "ReclassifiedWriter" 'func zzGuardTestReclassifiedWriter() string {
	return `UPDATE price_history SET ends_at = NULL`
}'
expect_pass "a writer once price_history is no longer classified in nonAdminTables" \
  "SYNC_CLASSIFICATION_FILE=${scratch_dir}/sync_admin_repo.go"
clear_fixtures

# Baseline: the guard must still pass on the real, unmodified codebase —
# every real writer (pos_repo.go's append pair and CleanupObsoleteItems,
# demo_seed_repo.go's RemoveDemoItem, catalog_repo.go's #2314 exec twins,
# and sync_admin_repo.go's own ADR-0099 invalidation UPDATE) must be
# correctly attributed to its allowlisted function. This is the end-to-end
# proof the attribution + allowlist actually work on real gofmt'd code.
if run_guard >/tmp/guard_price_history_test_out.$$ 2>&1; then
  echo "✓ guard still passes on the clean codebase (every real writer allowlisted)"
else
  echo "❌ FAIL: guard rejects the clean codebase (false positive introduced)" >&2
  cat /tmp/guard_price_history_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
rm -f /tmp/guard_price_history_test_out.$$

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-price-history-sync_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-price-history-sync_test.sh: all cases passed"
exit 0
