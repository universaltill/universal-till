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
mutated_real_file=""
mutated_real_file_backup=""
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
  # A real, tracked, allowlisted file (see the "trailing var attribution"
  # case below) — restored from its own pre-mutation backup unconditionally
  # here too, so a killed run mid-case can never leave it appended.
  if [[ -n "${mutated_real_file}" && -f "${mutated_real_file_backup}" ]]; then
    cp "${mutated_real_file_backup}" "${mutated_real_file}"
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
# The backtick Go raw-string literals below are plant()'s payload, not
# meant to expand in this shell — single-quoted deliberately.
# shellcheck disable=SC2016
plant "internal/pages" "pages" "UpdateWriter" 'func zzGuardTestUpdateWriter() string {
	return `UPDATE price_history SET ends_at = NULL WHERE item_id = ?`
}'
expect_fail "an UPDATE price_history planted outside internal/data"
clear_fixtures

# shellcheck disable=SC2016
plant "internal/pages" "pages" "InsertWriter" 'func zzGuardTestInsertWriter() string {
	return `INSERT INTO price_history(id, item_id, price, starts_at) VALUES(?,?,?,?)`
}'
expect_fail "an INSERT INTO price_history planted outside internal/data"
clear_fixtures

# shellcheck disable=SC2016
plant "internal/pages" "pages" "DeleteWriter" 'func zzGuardTestDeleteWriter() string {
	return `DELETE FROM price_history WHERE item_id = ?`
}'
expect_fail "a DELETE FROM price_history planted outside internal/data"
clear_fixtures

# A write planted INSIDE internal/data, in a new file, must ALSO be
# rejected — the allowlist is per file:function, not "anything in the data
# layer is fine" (guard-data-access.sh already allows raw SQL there; this
# guard is the narrower question of WHICH functions may write this table).
# shellcheck disable=SC2016
plant "internal/data" "data" "NewDataWriter" 'func zzGuardTestNewDataWriter() string {
	return `UPDATE price_history SET ends_at = CURRENT_TIMESTAMP WHERE ends_at IS NULL`
}'
expect_fail "a write inside internal/data but in a function not on the allowlist"
clear_fixtures

# Reusing an allowlisted function NAME in a different file must not pass:
# the key is file:function, so a same-named function elsewhere is a new,
# unreviewed writer.
# shellcheck disable=SC2016
plant "internal/data" "data" "NameSquatter" 'func AppendPriceHistoryItem() string {
	return `INSERT INTO price_history(id) VALUES(?)`
}'
expect_fail "an allowlisted function NAME reused in a non-allowlisted file"
clear_fixtures

# A package-level SQL string with no enclosing func can never be allowlisted
# (the unit of review is a named function) and must fail.
# shellcheck disable=SC2016
plant "internal/data" "data" "PackageLevel" 'var zzGuardTestPackageLevel = `DELETE FROM price_history WHERE 1`'
expect_fail "a package-level price_history write string with no enclosing func"
clear_fixtures

# Independent review (ADR-0099/ut-docs#2348 Opus pass) found the previous
# literal `(INSERT INTO|UPDATE|DELETE FROM) price_history` pattern blind to
# this codebase's own dominant insert idiom -- INSERT OR IGNORE/REPLACE
# INTO -- and to a bare extra space/tab before the table name. Two real,
# in-tree writers (scripts/e2e_seed, scripts/smoke_quickstart) already use
# exactly this idiom; see the scripts/** exclusion case below for why those
# two specifically don't need an ALLOWED_WRITERS entry. This block proves
# the widened pattern actually catches the idiom OUTSIDE that exclusion.
# shellcheck disable=SC2016
plant "internal/pages" "pages" "InsertOrIgnoreWriter" 'func zzGuardTestInsertOrIgnoreWriter() string {
	return `INSERT OR IGNORE INTO price_history (id,item_id,price,starts_at) VALUES (?,?,?,?)`
}'
expect_fail "an INSERT OR IGNORE INTO price_history planted outside internal/data"
clear_fixtures

# shellcheck disable=SC2016
plant "internal/pages" "pages" "InsertOrReplaceWriter" 'func zzGuardTestInsertOrReplaceWriter() string {
	return `INSERT OR REPLACE INTO price_history (id,item_id,price,starts_at) VALUES (?,?,?,?)`
}'
expect_fail "an INSERT OR REPLACE INTO price_history planted outside internal/data"
clear_fixtures

# shellcheck disable=SC2016
plant "internal/pages" "pages" "ReplaceIntoWriter" 'func zzGuardTestReplaceIntoWriter() string {
	return `REPLACE INTO price_history (id,item_id,price,starts_at) VALUES (?,?,?,?)`
}'
expect_fail "a bare REPLACE INTO price_history planted outside internal/data"
clear_fixtures

# Extra whitespace (a double space, standing in for any run of
# [[:space:]] including a real tab) between the keyword and the table name
# must not evade the guard either.
# shellcheck disable=SC2016
plant "internal/pages" "pages" "DoubleSpaceWriter" 'func zzGuardTestDoubleSpaceWriter() string {
	return `UPDATE  price_history SET ends_at = NULL`
}'
expect_fail "UPDATE<double-space>price_history planted outside internal/data"
clear_fixtures

# scripts/** is excluded from the scan entirely (test-support tooling, same
# carve-out CLAUDE.md's data-access rule already makes) -- a write planted
# there must NOT trip the guard, proving the exclusion is a deliberate,
# tested decision and not an accidental gap nobody noticed. This is the
# mirror image of the .claude/worktrees/ decoy case below: same mechanism
# (a path-prefix exclusion), opposite reason (test-support code, not a
# stale copy). Reuses the already-existing scripts/smoke_quickstart/
# directory rather than a new one -- plant() only creates the FILE, not
# its parent directory, and every other case above already has one.
# shellcheck disable=SC2016
plant "scripts/smoke_quickstart" "main" "ScriptsWriter" 'func zzGuardTestScriptsWriter() string {
	return `INSERT INTO price_history(id) VALUES(?)`
}'
expect_pass "a price_history write planted under scripts/ (excluded test-support scope)"
clear_fixtures

# Attribution is "nearest preceding top-level func", not brace-scope
# tracking (documented as a known limitation above, corrected from an
# earlier draft's false "can never be allowlisted" claim) -- a package-
# level declaration placed AFTER a REAL allowlisted function's closing
# brace is attributed to THAT function and must still pass. Verified
# against a real allowlisted file/function (there is no other way to
# exercise this against the actual ALLOWED_WRITERS list, which is keyed to
# real paths) -- backed up first and restored immediately after via both
# the explicit cp below and the EXIT trap's own unconditional restore.
real_allowlisted_file="internal/data/sync_admin_repo.go"
real_allowlisted_func="invalidateStalePriceHistoryOnSync"
mutated_real_file_backup="${scratch_dir}/sync_admin_repo.go.orig"
cp "${real_allowlisted_file}" "${mutated_real_file_backup}"
mutated_real_file="${real_allowlisted_file}"
# Computed, not hardcoded -- invalidateStalePriceHistoryOnSync is NOT the
# last function in this large file, so a plain `>>` append would land
# after some LATER function instead and attribute to that one, proving a
# true but different fact than intended. This finds that function's own
# closing brace (nearest `^}` at or after its `^func` line) and inserts
# immediately after THAT line specifically.
insert_after_line="$(awk -v fn="^func ${real_allowlisted_func}" \
  '$0 ~ fn {f=1} f && /^}/{print NR; exit}' "${real_allowlisted_file}")"
if [[ -z "${insert_after_line}" ]]; then
  echo "❌ FAIL: could not locate ${real_allowlisted_func}'s closing brace in ${real_allowlisted_file} (renamed? this case needs updating)" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  head -n "${insert_after_line}" "${real_allowlisted_file}" >"${scratch_dir}/sync_admin_repo.go.spliced"
  {
    echo ""
    # shellcheck disable=SC2016
    echo 'var zzGuardTestTrailingVarAfterAllowlistedFunc = `UPDATE price_history SET ends_at = NULL`'
  } >>"${scratch_dir}/sync_admin_repo.go.spliced"
  tail -n "+$((insert_after_line + 1))" "${real_allowlisted_file}" >>"${scratch_dir}/sync_admin_repo.go.spliced"
  cp "${scratch_dir}/sync_admin_repo.go.spliced" "${real_allowlisted_file}"
  expect_pass "a package-level var placed right after ${real_allowlisted_func}'s closing brace (attributed to it, per the documented limitation)"
fi
cp "${mutated_real_file_backup}" "${real_allowlisted_file}"
mutated_real_file=""

# Independent review finding: the allowlist had no liveness check -- an
# entry whose function no longer exists (renamed/deleted/rewritten) stayed
# silently valid forever, pre-approving whatever unrelated code next
# reused that exact file:function pair. Proven two ways:
#
# (a) the real, unmodified codebase must report nothing stale (all 7 real
#     ALLOWED_WRITERS entries genuinely still match) --
if run_guard >/tmp/guard_price_history_test_out.$$ 2>&1; then
  if grep -q 'stale' /tmp/guard_price_history_test_out.$$; then
    echo "❌ FAIL: clean codebase run unexpectedly flagged a stale ALLOWED_WRITERS entry:" >&2
    cat /tmp/guard_price_history_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard's stale-allowlist check finds nothing stale on the real, unmodified codebase (all 7 entries have a real match)"
  fi
else
  echo "❌ FAIL: guard rejected the clean codebase while checking for stale entries:" >&2
  cat /tmp/guard_price_history_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
rm -f /tmp/guard_price_history_test_out.$$

# (b) a genuinely stale entry must be caught. ALLOWED_WRITERS is hardcoded
#     inside the guard script itself (deliberately -- "never widen the
#     regex" from a lookup file), so this runs a MODIFIED COPY of the
#     guard, with one bogus entry appended, from a NEW file placed
#     alongside the real guard under scripts/ci/ -- it has to live there
#     (not in ${scratch_dir}) because the guard computes ROOT_DIR relative
#     to its own script path (dirname .../../..), which only resolves to
#     the real repo root from inside scripts/ci/. Tracked via `fixtures`
#     like every other planted file, so the EXIT trap removes it even on
#     an interrupted run; never touches ALLOWED_WRITERS in the real,
#     tracked guard.
scratch_guard="scripts/ci/zz_guard_test_scratch_stale_entry.sh"
fixtures+=("${scratch_guard}")
sed -E 's|^(ALLOWED_WRITERS=\()$|\1\n  "internal/data/does_not_exist.go:NoSuchFunction"  # planted stale entry, test-only|' \
  "${GUARD}" >"${scratch_guard}"
if ! grep -q 'does_not_exist.go:NoSuchFunction' "${scratch_guard}"; then
  echo "❌ FAIL: could not inject the test-only stale entry into the scratch guard copy (sed pattern drifted from ${GUARD}'s real ALLOWED_WRITERS declaration?)" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  if bash "${scratch_guard}" >/tmp/guard_price_history_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected the scratch guard (with one bogus ALLOWED_WRITERS entry) to reject as stale, but it passed" >&2
    cat /tmp/guard_price_history_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif ! grep -q 'stale ALLOWED_WRITERS entry' /tmp/guard_price_history_test_out.$$ || ! grep -q 'does_not_exist.go:NoSuchFunction' /tmp/guard_price_history_test_out.$$; then
    echo "❌ FAIL: scratch guard rejected, but not with the expected stale-entry message naming the planted entry:" >&2
    cat /tmp/guard_price_history_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly flags a genuinely stale ALLOWED_WRITERS entry (function renamed/deleted/never existed)"
  fi
fi
rm -f /tmp/guard_price_history_test_out.$$
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
# The backtick Go raw-string literal is this fixture's payload, not meant
# to expand in this shell — single-quoted deliberately.
# shellcheck disable=SC2016
printf 'package pages\n\nfunc zzGuardTestWorktreeDecoy() string {\n\treturn `UPDATE price_history SET ends_at = NULL`\n}\n' >"${worktree_decoy_path}"
expect_pass "a write planted under .claude/worktrees/ (agent worktree copy)"
rm -rf "${worktree_decoy_root}"
worktree_decoy_root=""

# A write planted in a _test.go file must not trip the guard (test
# fixtures INSERT into price_history all the time — mirrors the deadcode
# guard's own test-file exclusion).
path="internal/pages/zz_guard_test_TestFileWrite_test.go"
fixtures+=("${path}")
# shellcheck disable=SC2016
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
# shellcheck disable=SC2016
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
