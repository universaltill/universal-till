#!/usr/bin/env bash
#
# Regression test for guard-fixed-dates.sh (ut-docs#5467). Fixtures are
# planted in a sandbox copy (guard-test-sandbox.sh), with "today" pinned via
# UT_FIXED_DATE_TODAY so the test is never itself a time bomb.
set -euo pipefail

# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox scripts/ci
trap remove_guard_test_sandbox EXIT

GUARD="scripts/ci/guard-fixed-dates.sh"
TODAY=2026-11-04
FAIL_COUNT=0

reset_tree() {
  git rm -rq --cached --ignore-unmatch internal e2e vendor .claude >/dev/null 2>&1 || true
  rm -rf internal e2e vendor .claude
  mkdir -p internal/fx e2e/tests vendor/x .claude/worktrees/x
}

# plant <path> <content line>; stages it (force, so ignored paths are tracked).
plant() {
  mkdir -p "$(dirname "$1")"
  printf '%s\n' "$2" >"$1"
  git add -f "$1"
}

expect() {
  local want="$1" what="$2" got
  if UT_FIXED_DATE_TODAY="${TODAY}" bash "${GUARD}" "${PWD}" >out.txt 2>&1; then got=pass; else got=fail; fi
  if [ "$got" = "$want" ]; then
    echo "✓ guard → ${want}: ${what}"
  else
    echo "❌ FAIL: guard should ${want} ${what}, got ${got}" >&2
    cat out.txt >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

T=internal/fx/a_test.go
base() { plant internal/fx/ok_test.go 'var ok = 1'; }

reset_tree; base
plant "$T" 'var a = "2026-12-31T00:00:00Z"'
expect fail "an RFC3339 in-window date"
grep -q "file=${T},line=1" out.txt || { echo "❌ FAIL: output does not name file:line" >&2; FAIL_COUNT=$((FAIL_COUNT + 1)); }
plant "$T" 'var a = "2026-12-31"'
expect fail "a date-only in-window date"
plant "$T" "var a = \`select '2027-01-01T00:00:00Z'\`"
expect fail "a single-quoted SQL date inside a raw string"
plant "$T" 'var a = fmt.Sprintf("2026-12-01T%02d:00:00Z", 1)'
expect fail "a Sprintf format date"

reset_tree; base
plant "$T" 'var a = "2026-11-04"'
expect fail "today (lower boundary)"
plant "$T" 'var a = "2027-01-03"'
expect fail "today+60 (upper boundary)"
plant "$T" 'var a = "2027-01-04"'
expect pass "today+61"
plant "$T" 'var a = "2026-11-03"'
expect pass "yesterday"
plant "$T" 'var a = "2026-13-01"'
expect pass "an impossible month"

reset_tree; base
plant "$T" 'var a, b = "2026-01-01", "2026-12-31"'
expect fail "a second date on the same line"

reset_tree; base
plant "$T" 'var a = "2026-12-31" // fixed-date:ok display-only, never compared'
expect pass "a marker with a reason"
plant "$T" 'var a = "2026-12-31" // fixed-date:ok'
expect fail "a bare marker"
plant "$T" 'var a = "2026-12-31" // fixed-date:ok   '
expect fail "a marker followed only by spaces"
grep -q 'reason' out.txt || { echo "❌ FAIL: bare-marker message lacks 'reason'" >&2; FAIL_COUNT=$((FAIL_COUNT + 1)); }

reset_tree; base
plant "$T" '	// seeded on 2026-12-01 for the test'
expect pass "a comment-only line"
plant "$T" 'var a = 1 // 2026-12-01'
expect fail "a trailing comment on a code line (accepted false positive)"
plant "$T" '/* seeded 2026-12-01'
expect pass "a block comment's opening line"
plant "$T" '	 * seeded 2026-12-01'
expect pass "a block comment's middle line (review F2)"

reset_tree; base
plant "$T" 'var a = "2026-12-31"'
git commit -qm planted --no-verify >/dev/null 2>&1 || true
rm "$T"
expect pass "a tracked fixture deleted in the worktree (review F1)"

reset_tree; base
plant e2e/tests/x.spec.ts 'const d = "2026-12-31";'
expect fail "an e2e spec"
reset_tree; base
plant tests/e2e/tests/y.spec.ts 'const d = "2026-12-31";'
expect fail "a spec outside e2e/ (review F3)"
git rm -rqf tests >/dev/null 2>&1 || true
reset_tree; base
plant e2e/lib.js 'const d = "2026-12-31";'
plant internal/fx/x.go 'var a = "2026-12-31"'
expect pass "non-test files (out of scope)"

reset_tree; base
plant vendor/x/v_test.go 'var a = "2026-12-31"'
plant .claude/worktrees/x/w_test.go 'var a = "2026-12-31"'
expect pass "vendor/ and .claude/ paths"
printf 'internal/fx/ignored_test.go\n' >>.gitignore
printf 'var a = "2026-12-31"\n' >internal/fx/ignored_test.go
expect pass "an untracked gitignored fixture"
reset_tree; base
mkdir -p internal/fx && printf 'var a = "2026-12-31"\n' >internal/fx/new_test.go
expect fail "an untracked, not-ignored fixture (a new file before git add)"

reset_tree; base
if UT_FIXED_DATE_TODAY=garbage bash "${GUARD}" "${PWD}" >out.txt 2>&1; then rc=0; else rc=$?; fi
if [ "$rc" -eq 2 ]; then echo "✓ guard → exit 2: bad UT_FIXED_DATE_TODAY"; else echo "❌ FAIL: bad today exit ${rc}" >&2; FAIL_COUNT=$((FAIL_COUNT + 1)); fi

reset_tree
# scripts/ci carries its own Go tests; drop them so no test file remains.
git ls-files -z -- '*_test.go' '*.spec.ts' | xargs -0 -r git rm -qf --
expect fail "a tree with no test files"

if [ "$FAIL_COUNT" -ne 0 ]; then
  echo "${FAIL_COUNT} failure(s)" >&2
  exit 1
fi
echo "✓ guard-fixed-dates_test: all cases passed"
