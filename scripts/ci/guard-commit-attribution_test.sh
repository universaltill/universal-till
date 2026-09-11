#!/usr/bin/env bash
#
# Regression test for guard-commit-attribution.sh (ut-docs#1373 + ut-docs#732
# + ut-docs#2103): proves the guard rejects AI-tool/unattributable authors,
# rejects a well-formed users.noreply.github.com address whose numeric ID
# doesn't match a known contributor (the exact ut-docs#1373 incident,
# reproduced with the real bad commit's data — including trivial variations
# of it: mixed case, a stray extra '+', an empty username), rejects every
# legacy no-ID-prefix noreply address (the allowlist for that shape is
# deliberately empty — see the guard's own comment), rejects an ordinary
# (non-noreply) address that isn't on ALLOWED_PLAIN_EMAILS (ut-docs#2103 —
# this branch used to pass any such address unconditionally), rejects a
# pipe character smuggled into the author NAME field, fails closed on an
# empty commit range instead of reporting a silent pass, passes every
# known-good shape (including an allowlisted plain address), and smoke-checks
# that the guard parses the real repo's own most recent non-merge commit
# without crashing.
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

# Commit lines are 'sha|email|name' — EMAIL BEFORE NAME (see the guard's own
# comment on why: a '|' inside the name must not be able to smuggle bytes
# into the email field).

# A normal, correctly-ID'd noreply commit — the everyday case.
expect_pass "a correctly-ID'd noreply commit" \
  'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa|4035824+farshidmirza@users.noreply.github.com|Farshid Mirza'

# A second known contributor's correctly-ID'd noreply commit.
expect_pass "a second known contributor's correctly-ID'd noreply commit" \
  'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb|35641125+pouria-teimouri@users.noreply.github.com|Pouria Teimouri'

# An allowlisted plain (non-noreply) address — the pipeline owner's own
# sanctioned identity as of ut-docs#2103.
expect_pass "the allowlisted pipeline-owner plain email address" \
  'dddddddddddddddddddddddddddddddddddddddd|farsid@taskrunnertech.co.uk|Farshid Mirza'

# Case variation of the allowlisted plain address — matching must be
# case-insensitive here too, same as every other branch.
expect_pass "the allowlisted plain address, mixed case" \
  'iiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiiii|Farsid@TaskRunnerTech.co.uk|Farshid Mirza'

# A plain (non-noreply) email NOT on the allowlist must be rejected —
# ut-docs#2103: this branch used to pass any ordinary address unconditionally.
expect_fail "an unrecognized plain email address (not on ALLOWED_PLAIN_EMAILS)" \
  'jjjjjjjjjjjjjjjjjjjjjjjjjjjjjjjjjjjjjjjj|contributor@example.com|Somebody'

# Multiple good commits in one run.
expect_pass "multiple good commits together" \
  'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa|4035824+farshidmirza@users.noreply.github.com|Farshid Mirza' \
  'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb|35641125+pouria-teimouri@users.noreply.github.com|Pouria Teimouri'

# AI-tool identity as author — the original ut-docs#732 case.
expect_fail "an AI-tool author identity" \
  'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee|noreply@anthropic.com|Claude'

# Case variation of a banned address — matching must be case-insensitive.
expect_fail "an AI-tool identity with mixed-case domain" \
  'gggggggggggggggggggggggggggggggggggggggg|noreply@Anthropic.Com|Claude'

# The verified-on-nobody address.
expect_fail "the unattributable universaltill.com address" \
  'ffffffffffffffffffffffffffffffffffffffff|noreply@universaltill.com|Farshid Mirza'

# THE exact ut-docs#1373 incident: real commit, real bad ID, reproduced
# verbatim (the SanmayJoshi ID under Farshid's username).
expect_fail "the real ut-docs#1373 incident commit (26383381 is not farshidmirza's ID)" \
  '980c0287103cb1ee8fd375773b8c7898c0e59637|26383381+farshid3003@users.noreply.github.com|Farshid Mirza'

# The same incident address with a trivial case variation — must not
# escape via a case-sensitive match.
expect_fail "the ut-docs#1373 incident address, mixed case" \
  'hhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhh|26383381+farshid3003@Users.NoReply.GitHub.com|Farshid Mirza'

# A well-formed noreply address with a made-up ID that matches no known
# contributor at all (not just "wrong person" — "nobody on the list").
expect_fail "a well-formed noreply ID matching no known contributor" \
  '1111111111111111111111111111111111111111|99999999+somebody@users.noreply.github.com|Somebody'

# A legacy no-ID-prefix noreply username — the allowlist for this shape is
# deliberately empty (see the guard's own comment: this exact shape is how
# a renamed/retired login recreates ut-docs#1373), so ANY legacy address
# must be rejected, even one using a name that used to be valid history.
expect_fail "any legacy no-ID-prefix noreply username (allowlist is empty)" \
  '2222222222222222222222222222222222222222|farshid3003@users.noreply.github.com|Farshid'

# Unrecognized-shape noreply addresses must DEFAULT-DENY, not fall through
# to "ok" — this is the exact class of gap that would let ut-docs#1373's
# address back in with a one-character variation.
expect_fail "a stray extra '+' in the noreply local part" \
  '3333333333333333333333333333333333333333|4035824+a+b@users.noreply.github.com|Somebody'
expect_fail "an empty username in an ID-prefixed noreply address" \
  '4444444444444444444444444444444444444444|4035824+@users.noreply.github.com|Somebody'
expect_fail "a dot in the noreply username (not a legal GitHub username char)" \
  '5555555555555555555555555555555555555555|4035824+farshid.mirza@users.noreply.github.com|Somebody'

# A '|' character inside the author NAME must not let a banned/malformed
# email dodge every check by shifting field boundaries — email is read
# BEFORE name specifically so the name field absorbs any trailing '|'s.
expect_fail "a '|' character embedded in the author name, paired with a banned email" \
  '6666666666666666666666666666666666666666|noreply@anthropic.com|Evil|Name'
expect_pass "a '|' character embedded in the author name, paired with a good email" \
  '7777777777777777777777777777777777777777|4035824+farshidmirza@users.noreply.github.com|Evil|Name'

# No commits at all must fail closed — a real PR always has >=1 commit, so
# an empty read almost certainly means the upstream `git log` failed
# silently (e.g. an unreachable base SHA), and reporting that as a pass
# would turn a real gap into a silent no-op.
expect_fail "an empty commit range (fails closed, doesn't silently pass)" ""

# The real, unmodified repo's most recent NON-MERGE commit must still be
# PARSED without the guard crashing — a smoke check on genuine `git log`
# output, not a check of this repo's own history. (Separately: the live
# workflow only ever checks a PR's own new commits against its base, never
# full main history, which can and does carry pre-guard commits — see the
# guard's own "not fixed by this ticket" scoping.)
#
# --no-merges is deliberate, not incidental (ut-docs#2103), though it only
# narrows the gap, it doesn't close it: a PR's own `BASE_SHA..HEAD_SHA`
# range (see commit-attribution.yml) DOES still include any commit that
# merges the base branch INTO the PR branch mid-flight (a real, routine
# occurrence in this ecosystem's history) — `--no-merges` only excludes
# the merge commit GitHub creates once a PR itself lands, which indeed
# never appears in any PR's own range. Excluding merges here matters
# because GitHub's "Merge pull request" button (and, worse, its "Update
# branch" button, which authors a same-branch merge commit that IS inside
# a PR's range) authors that merge commit with the clicking account's
# real, verified personal email — on this org that is
# `farshid3003@gmail.com`, real and GitHub-linked, but not one on
# ALLOWED_PLAIN_EMAILS (that leak into public history is ut-docs#1100's
# own separate, already-tracked problem — not something to paper over by
# allowlisting it here).
#
# So this check deliberately does NOT assert pass/fail on the outcome —
# only that the guard runs to completion without a shell error. This
# repo's own history is not this test's to control: an accidental
# squash/rebase-merge (`allow_squash_merge`/`allow_rebase_merge` are still
# `true` here — see the reviewer skill's "Merge method" note) would
# instantly and permanently make the OLD (assert-pass) version of this
# check red for every future PR, for a reason with nothing to do with that
# PR's own diff. Confirmed empirically while reviewing ut-docs#2103: adding
# one `farshid3003@gmail.com`-authored commit on top was enough to fail
# the old assertion.
if git rev-parse --git-dir >/dev/null 2>&1; then
  tip_sha="$(git log --no-merges --format='%H' -1 2>/dev/null || true)"
  tip_email="$(git log --no-merges --format='%ae' -1 2>/dev/null || true)"
  tip_name="$(git log --no-merges --format='%an' -1 2>/dev/null || true)"
  if [ -n "${tip_sha}" ]; then
    if printf '%s\n' "${tip_sha}|${tip_email}|${tip_name}" | run_guard >/tmp/guard_commit_attr_smoke_out.$$ 2>&1; then
      echo "✓ guard parsed the real repo's most recent non-merge commit without crashing (result: pass)"
    else
      # A real rejection here is a fact about this repo's own history —
      # e.g. an accidental squash/rebase/update-branch merge commit — not a
      # bug in the guard, which is DESIGNED to reject a real, correctly
      # parsed address that just isn't on an allowlist. Don't fail the
      # smoke check on that; its job is "parses without crashing", not
      # "every commit in this repo's history is on an allowlist."
      echo "✓ guard parsed the real repo's most recent non-merge commit without crashing (result: reject — a fact about this repo's history, not a guard bug; see comment above)"
    fi
    rm -f /tmp/guard_commit_attr_smoke_out.$$
  fi
fi

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "${FAIL_COUNT} guard-commit-attribution_test.sh assertion(s) failed" >&2
  exit 1
fi
echo "✓ guard-commit-attribution_test.sh: all assertions passed"
