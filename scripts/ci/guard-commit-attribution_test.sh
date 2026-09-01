#!/usr/bin/env bash
#
# Regression test for guard-commit-attribution.sh (ut-docs#1373 + ut-docs#732):
# proves the guard rejects AI-tool/unattributable authors, rejects a
# well-formed users.noreply.github.com address whose numeric ID doesn't
# match a known contributor (the exact ut-docs#1373 incident, reproduced
# with the real bad commit's data), rejects an unknown legacy
# no-ID-prefix noreply username, and passes every known-good shape —
# including the real, unmodified repo's own history.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-commit-attribution.sh"
FAIL_COUNT=0

run_guard() {
  # stdin already set by the caller
  bash "${GUARD}"
}

expect_pass() {
  local label="$1"
  shift
  if printf '%s\n' "$@" | run_guard >/tmp/guard_commit_attr_test_out.$$ 2>&1; then
    echo "✓ guard correctly passed ${label}"
  else
    echo "❌ FAIL: expected guard to pass ${label}, but it rejected it" >&2
    cat /tmp/guard_commit_attr_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_commit_attr_test_out.$$
}

expect_fail() {
  local label="$1"
  shift
  if printf '%s\n' "$@" | run_guard >/tmp/guard_commit_attr_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_commit_attr_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_commit_attr_test_out.$$
}

# A normal, correctly-ID'd noreply commit — the everyday case.
expect_pass "a correctly-ID'd noreply commit" \
  'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa|Farshid Mirza|4035824+farshidmirza@users.noreply.github.com'

# A second known contributor's correctly-ID'd noreply commit.
expect_pass "a second known contributor's correctly-ID'd noreply commit" \
  'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb|Pouria Teimouri|35641125+pouria-teimouri@users.noreply.github.com'

# A known legacy (no-ID-prefix) noreply username.
expect_pass "a known legacy no-ID-prefix noreply username" \
  'cccccccccccccccccccccccccccccccccccccccc|Farshid|farshid3003@users.noreply.github.com'

# A verified personal (non-noreply) email — untouched by this guard.
expect_pass "a verified personal email address" \
  'dddddddddddddddddddddddddddddddddddddddd|Farshid Mirza|farshid3003@gmail.com'

# Multiple good commits in one run.
expect_pass "multiple good commits together" \
  'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa|Farshid Mirza|4035824+farshidmirza@users.noreply.github.com' \
  'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb|Pouria Teimouri|35641125+pouria-teimouri@users.noreply.github.com'

# No commits at all — nothing to check, not a failure.
expect_pass "an empty commit range" ""

# AI-tool identity as author — the original ut-docs#732 case.
expect_fail "an AI-tool author identity" \
  'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee|Claude|noreply@anthropic.com'

# The verified-on-nobody address.
expect_fail "the unattributable universaltill.com address" \
  'ffffffffffffffffffffffffffffffffffffffff|Farshid Mirza|noreply@universaltill.com'

# THE exact ut-docs#1373 incident: real commit, real bad ID, reproduced
# verbatim (the SanmayJoshi ID under Farshid's username).
expect_fail "the real ut-docs#1373 incident commit (26383381 is not farshidmirza's ID)" \
  '980c0287103cb1ee8fd375773b8c7898c0e59637|Farshid Mirza|26383381+farshid3003@users.noreply.github.com'

# A well-formed noreply address with a made-up ID that matches no known
# contributor at all (not just "wrong person" — "nobody on the list").
expect_fail "a well-formed noreply ID matching no known contributor" \
  '1111111111111111111111111111111111111111|Somebody|99999999+somebody@users.noreply.github.com'

# A legacy no-ID-prefix noreply username that isn't on the allowlist.
expect_fail "an unknown legacy no-ID-prefix noreply username" \
  '2222222222222222222222222222222222222222|Somebody|somebody-else@users.noreply.github.com'

# The live workflow only ever checks a PR's OWN new commits against its
# base, never full main history — so "does all of history pass" is not the
# right invariant to dogfood (main can and does carry pre-CI-existence
# commits this guard would reject if run retroactively — by design not
# rewritten, see #1373's own "not fixed by this ticket" scoping). Smoke-test
# against this repo's real HEAD commit instead: proves the guard parses real
# `git log` output (real SHAs, real names, real punctuation) without
# crashing, and that the tip of `main` — which passed CI to get there —
# actually passes.
if git rev-parse --git-dir >/dev/null 2>&1; then
  tip_log="$(git log --format='%H|%an|%ae' -1 2>/dev/null || true)"
  if [ -n "${tip_log}" ]; then
    expect_pass "the real repo's current HEAD commit" "${tip_log}"
  fi
fi

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "${FAIL_COUNT} guard-commit-attribution_test.sh assertion(s) failed" >&2
  exit 1
fi
echo "✓ guard-commit-attribution_test.sh: all assertions passed"
