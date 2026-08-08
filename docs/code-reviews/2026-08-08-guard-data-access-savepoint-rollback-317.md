# Code review: guard-data-access.sh doesn't recognize SAVEPOINT/ROLLBACK TO/RELEASE/BEGIN/COMMIT

**Ticket:** universaltill/ut-docs#317
**Date:** 2026-08-08
**Repo/branch:** `universal-till` / `fix/guard-data-access-savepoint-rollback`
**Reviewer:** independent fresh-context Sonnet subagent (complexity:easy tier)

## What changed

`scripts/ci/guard-data-access.sh` enforces the repository-pattern rule
(raw SQL text may only appear in `internal/data`/`internal/db`) by
regex-matching SQL keywords. It didn't match transaction-control
statement text — `SAVEPOINT`, `ROLLBACK TO`, `RELEASE`, `BEGIN`,
`COMMIT` — so a raw one of those planted outside the data layer passed
the guard silently. Found during ut-docs#310's review round 2, where
this had to be verified by hand with `grep` instead of the guard itself.

- Extended both the same-line and multi-line-backtick regexes to also
  catch the five statement types, with a word-boundary requirement on
  each new keyword.
- Added `scripts/ci/guard-data-access_test.sh`: plants each statement
  type as a fixture outside `internal/data`, asserts the guard rejects
  it, asserts the guard still passes on the clean codebase.
- Wired the new test into `.github/workflows/ci.yml`, right after the
  guard step itself.

## Review round 1 — findings (both fixed, both blocking)

An independent fresh-context Sonnet subagent reviewed the diff
adversarially: ran the guard/test itself, reverted just the regex fix to
confirm the new test actually goes red against the old behavior, and
grepped the real codebase in both directions for false-positive/
false-negative risk. It found two real bugs in the first draft of the
regex, both reproduced with concrete fixtures:

1. **False negative** — `line_start`'s `BEGIN`/`COMMIT` alternatives
   required a space or end-of-line immediately after the keyword, so
   `BEGIN;`/`COMMIT;` (the standard semicolon-terminated form) as the
   first token on its own line inside a multi-line raw-string query
   slipped past undetected. The original test never exercised this path
   — its `plant()` helper always put the statement on the same line as
   the backtick, only ever hitting `same_line`.
2. **False positive** — `same_line`'s new keywords had no trailing
   word-boundary at all, so ordinary Go identifiers that merely *start*
   with one of the five keywords (`"RELEASE_CANDIDATE"`,
   `"COMMITMENT_LEVEL_HIGH"`, `"BEGINNER_MODE"`) tripped the guard as
   "inline SQL found" even though none of them are SQL.

## Fix

Both regexes now require a word-boundary right after each of the five
new keywords: `[^A-Za-z0-9_]|$` in `same_line`, `[[:space:];]|$` in
`line_start` (adding `;` there specifically to cover the semicolon-
terminated bare-statement case). The existing keywords (SELECT/INSERT/
UPDATE/DELETE/CREATE TABLE) were left untouched — same pre-existing
looseness, out of scope for this ticket, unchanged behavior.

Two new regression cases were added to `guard-data-access_test.sh`:
- a multi-line fixture with `BEGIN;`/`COMMIT;` as the first token on
  their own line (`expect_fail`)
- a fixture with the three false-positive identifiers above
  (`expect_pass`, a new helper — the original test only had
  `expect_fail`)

Both were verified red against the pre-fix regex (via `git stash` on
just the guard script, keeping the new test) and green against the fix,
before amending the commit. `git status --porcelain` confirmed no
leftover fixture files after every run, including the failing ones.

## Verification performed (this session, after the fix)

- `go build ./...`, `go vet ./...` — clean.
- `go test ./...` — all packages pass except the pre-existing,
  unrelated `TestSaveCleansUpDirectoryOnWriteFailure`
  (`internal/issuereport`), which fails when `go test` runs as root —
  confirmed present on `origin/main` before this change too (tracked
  separately as ut-docs#415; not touched by this diff).
- `bash scripts/ci/guard-data-access.sh` — passes on the real codebase.
- `bash scripts/ci/guard-data-access_test.sh` — all 8 cases pass.
- Manually reproduced both review findings against the fixed script
  (planted the exact fixtures from the review) to confirm both are
  actually fixed, not just asserted fixed by the new test.

## Scope

CI-tooling-only change (bash + YAML), no Go application code, no
user-facing surface — no i18n/money/offline-first/manual-doc updates
apply.

## Outcome

Both round-1 findings fixed and re-verified red→green. No second review
round requested — this round's findings were fixed and the fix itself
re-verified directly (revert-and-reproduce) rather than by grinding
another model pass on an unchanged regex-completeness question.
