# Code review: internal/alerts test depended on the real wall clock (ut-docs#1769)

**Date:** 2026-09-08
**Card:** ut-docs#1769 (`complexity:easy`)
**Diff:** `internal/alerts/alerts.go`, `internal/alerts/alerts_test.go`

## What was broken

`internal/alerts.Start`'s digest loop called `unusualSales(ctx, db,
time.Now())` directly — a live read of the real wall clock, with no seam a
test could control. `unusualSales`/`POSRepo.DayTotal` bucket sales by LOCAL
calendar day (the SQL `'localtime'` modifier), while
`TestStart_RunsDigestLoopBody` seeded its unusual-sales baseline anchored to
a UTC-truncated `noon` instant computed once at test setup. Those two reads
— the seed anchor and Start's own live `time.Now()` a couple of
milliseconds later — only ever agreed on "today" by coincidence: whenever
they fell on different local calendar dates (routine in a non-UTC
timezone, and possible even in UTC right at a day boundary), the seeded
baseline silently misaligned with what the query looked for and the
unusual-sales push never fired. Reported reproducing at 00:15 local time,
confirmed independent of CI (which happened to always run in UTC at a
different hour).

## The fix

- `internal/alerts/alerts.go`: a new `nowOverride atomic.Pointer[time.Time]`
  package var, read through a `now()` helper that falls back to real
  `time.Now()` when nil (always true in production — nothing outside the
  test file ever sets it). `Start`'s loop now calls `now()` instead of
  `time.Now()`. Same test-seam idiom already used in this file for
  `firstDelayNS`/`tickIntervalNS` (atomic, not a plain var, because the
  loop goroutine reads it independently of a test's write).
- `internal/alerts/alerts_test.go`: `TestStart_RunsDigestLoopBody` now pins
  `nowOverride` to a fixed synthetic instant (`anchor`) and seeds the
  unusual-sales baseline against that *same* instant, instead of an
  independent live read — removing the wall-clock dependency entirely. The
  low-stock seed row (`'sl'`) deliberately stays anchored to the real clock
  (`datetime('now','-1 days')`): it feeds `runningOutCount`'s rolling
  28-day window, which is not calendar-bucketed and was never the flaky
  half of this test — routing it through `nowOverride` too would have
  broken it against the real 28-day window whenever the synthetic anchor
  drifted far from the actual run date (caught during Dev: an early draft
  did exactly this and every subtest failed on the low-stock digest, not
  the unusual-sales one).
- The test is now a table over 5 anchor hours (00/06/12/18/23 UTC),
  demonstrating the fix holds at any hour per AC2, not just whichever hour
  happened to be convenient.

## What was verified beyond automated tests

- **TDD, confirmed failing pre-fix**: reverted `now()` back to a direct
  `time.Now()` call in `Start`'s loop and re-ran the new table-driven
  test — all 5 anchor-hour subtests failed deterministically ("Start's
  loop never pushed an unusual-sales notification within 2s"), then
  restored the fix and confirmed all 5 pass. Verified independently a
  second time by the reviewer subagent (below), including its own
  restore-and-diff-check that the working tree matched the original fix
  exactly afterward.
- **Race safety**: `go test ./internal/alerts/... -race -count=3 -v` —
  clean, no race on `nowOverride` (Store happens before the loop
  goroutine is spawned; the test's cleanup Store(nil) happens only after
  `wg` has joined).
- **No other caller affected**: `nowOverride` is set nowhere outside
  `alerts_test.go` (repo-wide grep); the only production caller of
  `alerts.Start` is `internal/app/app.go:304`, so production behavior
  (`now() == time.Now()`) is unchanged.
- **Independent review** (fresh-context Sonnet subagent, per
  `complexity:easy` routing): verdict **PASS**, no blocking findings.
  Flagged one non-blocking style nit — the pre-existing local `now :=
  time.Now().Add(time.Second)` in `runningOutCount` shadowed the new
  package-level `now()` helper — fixed by renaming it to `nowPad` (its
  actual role: a 1-second pad on the sell-rate window's upper bound) and
  documenting why that path deliberately stays on the real clock.
- Full gate: `gofmt -l .` (clean), `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` (0 issues), `go test ./...` (full suite
  green), `scripts/ci/guard-data-access.sh` (clean — no SQL text added
  outside `internal/data`/`internal/db`).
- No UI/visible surface touched — this is a pure backend Go test-seam
  fix, so no screenshot/visual-check attestation applies.

## Scope note

The card's own AC1/AC2 are about the clock-dependence of this one test;
`unusualSales`'s actual day-bucketing correctness is already covered by
`TestUnusualSales` and `TestUnusualSales_ThinBaselineIsNotUnusual`, and
`POSRepo.DayTotal`'s local-calendar-day semantics by
`TestPOSRepo_DayTotal_LocalCalendarDays` — none of those needed touching
here.
