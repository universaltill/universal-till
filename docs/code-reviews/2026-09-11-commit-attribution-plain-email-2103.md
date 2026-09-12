# Code Review — commit-attribution guard: fail closed on an unlisted plain email

Date: 2026-09-11
Issue: universaltill/ut-docs#2103
Repos touched together: `ut-docs`, `universal-till`, `ut-cloud`, `ut-infra`
(the guard is duplicated byte-for-byte across all four; this record mirrors
the full record in `ut-docs/code-reviews/2026-09-11-commit-attribution-plain-email-2103.md`,
which has the complete narrative — see that copy for the fullest detail)
Reviewer: independent Opus subagent (complexity:medium → Sonnet dev / Opus
review, per `scrum-master/MODEL-ROUTING.md`)

## What shipped (this repo's copy)

`scripts/ci/guard-commit-attribution.sh`'s branch 5 ("anything else") passed
any ordinary, non-noreply email address unconditionally. Added
`ALLOWED_PLAIN_EMAILS` (seeded with `farsid@taskrunnertech.co.uk`) with the
same default-deny posture as the existing `ALLOWED_IDS`/
`ALLOWED_LEGACY_USERNAMES` allowlists; branch 5 now fails closed on a miss.
Updated the header comment and failure message accordingly, and
`scripts/ci/guard-commit-attribution_test.sh`'s regression suite: replaced
the old "a verified personal email address" pass-case (which asserted the
exact behaviour being closed) with a pass case for the allowlisted address
and a fail case for an unlisted one.

## What the independent review found

**Verdict: PASS, with 4 issues raised — all fixed identically across all 4
repos before merge.** Full detail of each finding and its fix is in the
`ut-docs` copy of this record (same date/topic); summary:

1. Failure-message remediation text didn't cover the new branch-5 case —
   fixed (diagnosis + remediation text expanded).
2. The `--no-merges` smoke-test rationale comment overclaimed (a
   merge-base-into-branch commit, e.g. via GitHub's "Update branch"
   button, IS inside a PR's own commit range) — fixed, claim narrowed.
3. **Most serious**: the smoke check asserted a pass/fail *policy verdict*
   on this repo's own live tip commit, which the reviewer showed could go
   permanently red on an accidental squash/rebase merge, unrelated to any
   given PR's diff — fixed, now only asserts the guard runs without
   crashing.
4. The new header comment overclaimed that a plain address now "routes
   every pipeline commit" through the unchecked branch, contradicting the
   still-standing per-cycle noreply-identity default in
   `scrum-master/SKILL.md` — softened to "may now hit."

## Verified beyond automated tests

- **TDD claim independently re-verified**: reverted only the guard script
  (kept the new tests), re-ran, confirmed the new fail-case genuinely fails
  pre-fix; restored, confirmed full suite passes.
- This repo's test suite re-run after all 4 fixes: green.
- `bash -n` clean on both touched files.
- `gofmt -l .` clean, `go build ./...` green (change is shell-only; this
  confirms no incidental Go breakage).
- `shellcheck` not installed in this session's environment — not run
  locally; CI's `shellcheck scripts/ci/*.sh` step is the first real run.
  The new code mirrors the exact array+loop-helper pattern already used by
  `is_allowed_id`/`is_allowed_legacy_username`, which already pass that gate.

## Safe-to-merge verdict

Safe to merge. No blocker-class issue remains.
