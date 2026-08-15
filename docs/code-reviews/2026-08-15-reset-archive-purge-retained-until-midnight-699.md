# Reset-archive purge: retained-until date accurate from midnight (ut-docs#699)

**Date:** 2026-08-15
**Card:** universaltill/ut-docs#699
**Complexity:** easy
**Repo/area:** `universal-till` — `internal/data/reset_archive_repo.go`

## What shipped

`DeleteResetBatch` refuses to permanently purge an archived reset-batch
that still holds trading history until the shop's statutory retention
window (`archive_min_days`) has elapsed since the batch was archived. The
refusal computed `retainedUntil` from the archive's **full** timestamp
(`archivedAt.AddDate(0, 0, minDays)`), including time-of-day — but both
the error message (`ArchiveWithinRetentionWindowError.Error()`) and the
API's operator-facing response format `RetainedUntil` as a bare date
(`"2006-01-02"`). Net effect: a batch archived at 14:30 stayed refused
until 14:30 on the named date, silently contradicting the date-only
message, which reads as "purgeable from midnight."

Fix: truncate `archivedAt` to its own UTC midnight before adding
`minDays`, so `retainedUntil` itself lands at midnight of the displayed
date — matching the granularity of what's actually shown to the
operator.

Files changed:
- `internal/data/reset_archive_repo.go` — the truncation fix (5 lines).
- `internal/data/reset_archive_delete_test.go` — new regression test
  `TestDeleteResetBatch_RetainedUntilDateIsPurgeableFromMidnight`,
  proving a purge attempted on the retained-until date itself, before
  the original archive's time-of-day, now succeeds.

## Independent review

Fresh-context Sonnet subagent (per `complexity:easy` routing), isolated
worktree, read-only pass plus actually running build/vet/tests/guards.

**Verdict: safe to merge, no blocking findings.**

Confirmed:
- The truncation is correct given `reset_batches.created_at` is always
  written as `time.Now().UTC().Format(time.RFC3339)` — no DST/offset
  risk, this is an all-UTC pipeline.
- Existing boundary tests (`WithinRetentionWindow_Refused`,
  `OutsideRetentionWindow_Deletes`, `BoundaryEitherSideOfCountryWindow`,
  `UnknownCountryFallsBackToGlobalFloor`, plus the API-level
  `TestResetArchivesPurge_CountryConfiguredWindow` and its 10-year-floor
  sibling in `internal/pages/data_api_test.go`) all use ≥1-day margins
  either side of the window, so they remain correct under the truncated
  semantics rather than merely passing by coincidence.
- No SQL added outside `internal/data`; no money type touched; no new
  user-facing strings; no template/locale/help-topic files touched — so
  the repository-pattern, money, and i18n non-negotiables didn't need
  touching, and the review confirmed that's actually true rather than
  assumed.
- No client/shop name or secret-shaped literal introduced.

**Independent TDD re-verification:** the reviewer reverted only the fix
in `reset_archive_repo.go` (leaving the new test untouched), reran the
new test, and confirmed it fails with exactly the claimed error
(`"...within its statutory retention window until <today>..."` while
attempting a purge on that same today). Restored the fix and confirmed
all 7 `TestDeleteResetBatch*` tests pass again, worktree diff
byte-identical to the pre-review commit.

**One non-blocking finding, fixed same-session:** the new test derived
its "slightly later than now" archive time-of-day via a plain
`now.Add(2 * time.Second)`, which — in the last ~2 seconds of any UTC
day — wraps past midnight, moving the wrapped time-of-day *behind* real
"now" and silently making even the pre-fix buggy arithmetic pass
(losing the test's regression-catching power in that ~0.002%-of-runs
window, not causing a spurious failure). Hardened by clamping the offset
to stay within the same calendar day instead of wrapping. Re-ran
`go build ./...`, `go vet ./...`, and the full `TestDeleteResetBatch*`
suite after the fix — all green; did not re-run the full review round
since this was a same-session hardening of a non-blocking nit, not a
new finding requiring independent re-verification.

## Verified beyond automated tests

- `go build ./...` and `go vet ./...` clean.
- `go test ./internal/data/... ./internal/pages/...` (full packages, not
  just the touched test) green.
- `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-kiosk-engine.sh`,
  `bash scripts/ci/guard-plugin-menu-read.sh` all pass (none of these
  guard classes are actually implicated by this diff — no new SQL, no
  self-order route, no plugin-menu read — confirmed rather than assumed
  by running them anyway).

## Deferred / out of scope

Nothing deferred — the card's acceptance criteria are fully met by this
change; no follow-up card needed.
