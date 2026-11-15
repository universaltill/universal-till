# Review: unusual-sales tests anchor their days in local time (ut-docs#5100)

## What shipped

Test-only change in `internal/alerts/alerts_test.go`. Production code is
unchanged.

- **Cause.** `POSRepo.DayTotal` matches
  `local_date = date(ref, 'localtime', '-N days')`, which is N **local**
  calendar days back. The test fixtures seeded each sale at
  `ref.AddDate(0, 0, -N)` on a UTC time, which is N×24h of UTC. Across a
  DST change, near local midnight, these land on different local dates.
  Example: ref 2026-10-26 23:15Z is 23:15 GMT on Oct 26. Eight days back in
  UTC is 2026-10-18 23:15Z, which is 00:15 BST on Oct 19, but `DayTotal`
  looks for Oct 18. The baseline then has fewer than 3 weeks, and
  `TestUnusualSales` reported "zero day … unusual=false" on main in the
  hours after the UK clock change.
- **Fix.** A new helper, `localNoonDaysBack(ref, n)`, returns the real-UTC
  `created_at` of local noon on the local day n days before ref's local
  day. Every `sale()` seeder in the file now uses it:
  - `TestUnusualSales` and the weekday sweep, which now share
    `runUnusualSalesScenario`;
  - `TestUnusualSales_ThinBaselineIsNotUnusual`;
  - `TestStart_RunsDigestLoopBody`.
- **Regression test.** `TestUnusualSales_AcrossDSTChange` re-runs itself in a
  subprocess with `TZ=Europe/London`, the same pattern as
  `TestArchiveReport_GuardComparesLocalDayNotUTCDay`. A subprocess is needed
  because modernc sqlite caches `localtime`. The child fails unless
  `time.Local` really is a DST zone. It runs the scenario at 8 refs around
  both 2026 UK clock changes, including the card's exact instant.

## Review

Built by Sonnet. Independent review by Opus 5.5 in a separate worktree.

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | `TestStart_RunsDigestLoopBody` had the same seeding bug. Its anchor of 2026-03-15 falls one week after US DST starts, so with `TZ=America/Denver` the `anchor_hour=06` case failed on every date. Reproduced. | should-fix | Fixed: it now uses `localNoonDaysBack` |
| 2 | `TestUnusualSales_ThinBaselineIsNotUnusual` used the same UTC seeding. It could not fail, but near DST it passed without testing anything. | low | Fixed: it now uses `localNoonDaysBack` |
| 3 | 4 of the first 6 regression refs could not tell the two seeding rules apart, and none covered local 00:00–01:00 BST after spring-forward. | should-fix | Fixed: added 2026-03-29 23:30Z and 2026-04-05 23:30Z, both of which fail under the old seeding |
| 4 | Root cause confirmed. Production `unusualSales`/`DayTotal` is plain local-calendar arithmetic and correct. A DST day really trades 23h or 25h, which is about ±4%, well inside the 0.4–1.8 thresholds. | info | No product change |
| 5 | Without tzdata, the subprocess fails hard rather than skipping. This matches the precedent. | info | Accepted |

## Verified

- TDD, re-run by the orchestrator: with `localNoonDaysBack` reverted to UTC
  `AddDate`, `TestUnusualSales_AcrossDSTChange` fails at 4 of its 8 refs
  (2026-10-25 23:30Z, 2026-10-26 23:15Z, 2026-03-29 23:30Z, 2026-04-05
  23:30Z), and `TZ=America/Denver … TestStart_RunsDigestLoopBody` fails.
  With the fix restored, both pass.
- `go test ./internal/alerts -count=1` is `ok` under `TZ=` UTC,
  Europe/London, America/Denver, America/New_York, Pacific/Kiritimati and
  Australia/Lord_Howe.
- `gofmt`, `go vet` and `golangci-lint` (alerts) are clean, `go build ./...`
  succeeds, and the full `go test ./...` and `-race` on alerts were run
  before the PR.

**Verdict:** safe to merge. Nothing deferred.
