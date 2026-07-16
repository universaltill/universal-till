# Code review — EOD Z-report department + till breakdown (E1b)

**Date:** 2026-07-16 · **Branch:** feat/eod-department-till-breakdown

## What
The end-of-day Z-report (`EODReport`/`EndOfDay`) now carries per-department and
per-till breakdowns, and the printed Z-report shows them:
- `EODReport.Departments` — sales by department for the day (reuses
  `DepartmentsForDay`, the recursive top-level-category rollup from E1).
- `EODReport.Tills` — sales by register for the day (only populated when >1 till
  was used, so single-register shops see no noise).
- `buildEODDoc` renders both as footer sections ("BY DEPARTMENT" / "BY TILL") so
  they print without changing the `print.Doc` struct.

Both are captured in the archived EOD JSON, so they also reach ask-your-till and
report exports. For department stores this is the daily per-floor / per-register
cash-up on the Z-report.

## Scope / notes
- Data + print-doc assembly only; no `internal/print` struct change.
- The printed EOD remains English (matches the existing `buildEODDoc` convention;
  full EOD-print localization is a separate, pre-existing gap).

## Tests
`go build ./...`, `go test ./...` (data + pages green), `guard-data-access.sh`
green.
