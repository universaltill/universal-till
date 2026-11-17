#!/usr/bin/env bash
# Self-test for the wip-commits guard next to this file (ut-docs#5943).
# Feeds `git log --format='%h %s'`-style lines on stdin; no git, no repo paths.
set -uo pipefail

GUARD="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/guard-wip-commits.sh"
OUT="$(mktemp)"
trap 'rm -f "$OUT"' EXIT
fails=0

run() { printf '%s\n' "$@" | bash "$GUARD" >"$OUT" 2>&1; }

expect_pass() {
  local name="$1"; shift
  if run "$@"; then echo "ok   $name"; else echo "FAIL $name: expected exit 0"; cat "$OUT"; fails=$((fails + 1)); fi
}
expect_fail() {
  local name="$1"; shift
  if run "$@"; then echo "FAIL $name: expected exit 1"; cat "$OUT"; fails=$((fails + 1)); else echo "ok   $name"; fi
}
expect_output_contains() {
  local name="$1" needle="$2"
  if grep -qF -- "$needle" "$OUT"; then echo "ok   $name"; else echo "FAIL $name: output lacks '$needle'"; cat "$OUT"; fails=$((fails + 1)); fi
}
expect_output_lacks() {
  local name="$1" needle="$2"
  if grep -qF -- "$needle" "$OUT"; then echo "FAIL $name: output has '$needle'"; cat "$OUT"; fails=$((fails + 1)); else echo "ok   $name"; fi
}

expect_fail a_plain "abc1234 WIP: pre-review snapshot"
expect_fail b_with_issue "abc1234 WIP: pre-review snapshot (ut-docs#5843)"
expect_fail c_case_insensitive "abc1234 wip: Pre-Review Snapshot"
expect_fail d_snapshot_at_end "abc1234 WIP: X report print (ut-docs#5798) pre-review snapshot"
expect_pass e_real_subject "abc1234 reference: X report spec draft, pre-review (#5780)"
expect_pass f_not_anchored "abc1234 docs: the WIP: pre-review snapshot step"
expect_pass g_other_wip "abc1234 WIP: tester cases for catimport guard"

expect_fail h_middle "aaa1111 feat: real work (#1)" "bbb2222 WIP: pre-review snapshot (ut-docs#5943)" "ccc3333 fix: another (#2)"
expect_output_contains h_sha "bbb2222"
expect_output_contains h_subject "WIP: pre-review snapshot (ut-docs#5943)"
expect_output_contains h_reset "git reset --soft"
expect_output_contains h_lease "--force-with-lease"
expect_output_lacks h_good1 "aaa1111"
expect_output_lacks h_good2 "ccc3333"

if printf '' | bash "$GUARD" >"$OUT" 2>&1; then echo "ok   i_empty"; else echo "FAIL i_empty"; cat "$OUT"; fails=$((fails + 1)); fi

expect_pass j_pipe_clean "abc1234 fix: a|b pipe (#1)"
expect_fail j_pipe_bad "abc1234 WIP: pre-review snapshot | x"
expect_output_contains j_full_subject "WIP: pre-review snapshot | x"
expect_fail k_leading_ws "abc1234   WIP: pre-review snapshot"
expect_pass l_sha_only "abc1234"

if [ "$fails" -ne 0 ]; then echo "❌ $fails guard-wip-commits test(s) failed"; exit 1; fi
echo "✓ all guard-wip-commits tests passed"
