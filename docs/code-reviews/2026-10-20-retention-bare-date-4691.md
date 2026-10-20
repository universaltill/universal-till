# Review: a legacy bare-date report period renders in the locale's date format (ut-docs#4691)

- **Date:** 2026-10-20
- **Branch:** `fix/4691-retention-bare-date`
- **Author:** lane:cloud-41 dev subagent (complexity:easy builder). **Reviewer:** a different model, fresh context.

## What shipped

Settings → Report retention prints "Records held: <earliest> – <latest>"
from `MIN(period)` / `MAX(period)` over `report_archive`, each through the
`datetime` template helper (ut-docs#3566). A legacy `eod` row's period is a
bare `YYYY-MM-DD` (pre ADR-0066 D5). `datetime` only parsed RFC3339 and
SQLite's `datetime('now')` layout, so that value came back raw: on a fa till
`2026-08-03 – ۲۱/۰۹/۲۰۲۶ ۱۹:۴۰`.

- `internal/httpx/dateformat.go`: new `ParseStoredDate` — a trimmed bare
  `2006-01-02` calendar date, parsed as UTC.
- `internal/httpx/httpx.go`: `datetime`'s string branch falls back to
  `ParseStoredDate` and renders `FormatDate(d, locale)` — date only, no
  clock, no `Local()` conversion (a calendar date has no instant, so a zone
  west of UTC must not move it to the day before).
- `ParseStoredTime` is deliberately not widened: `shortdatetime` and
  `datetimeUTC` use it and would show a fake `00:00`.

No template, locale key, CSS or SQL change. Every other `{{ datetime }}`
caller (about 25: journal, audit, shifts, orders, vouchers, tills roster, …)
takes a stamped-at-write timestamp, never a bare date, so nothing else
changes on screen.

## Tests (TDD)

- `internal/httpx/httpx_test.go` `TestFuncsForDateTimeBareDate_4691`, with
  `time.Local` pinned to America/Los_Angeles: `"2026-08-03"` renders
  `۰۳/۰۸/۲۰۲۶` (fa), `03.08.2026` (de-DE), `08/03/2026` (en-US); padded input
  the same; garbage stays raw; RFC3339 keeps its clock.
  `TestFuncsForExposesDateTime`'s "bare date stays raw" case now uses a
  genuinely unparseable string (changed on purpose).
- `internal/pages/settings_retention_coverage_3566_test.go`: new
  `TestSettingsRetentionCoverage_LegacyBareDateLocalized_4691` (bare-date
  earliest + RFC3339 latest, fa and en, both in the locale's convention).
  `…UnparseablePeriodStaysVisible_3566` updated on purpose to seed truly
  unparseable periods, which still render verbatim and isolated.
  `…IsolatedAndLocalized_3566` unchanged and green.

Both new tests failed before the `httpx.go` change (`earliest =
"2026-08-03", want "۰۳/۰۸/۲۰۲۶"`) and pass after. The reviewer re-verified
this independently in a separate worktree (revert hunk → fail → restore →
pass).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The page-level test doesn't pin `time.Local`, so a regression adding `.Local()` would only be caught by the httpx unit test (which pins LA). | Accepted: the unit test covers it. |
| 2 | nit | `{{ date }}` still returns a bare date raw; `datetime` is now more lenient. No template passes a bare date to `date`. | Accepted; `datetime`'s comment states the exception. |
| 3 | nit (pre-existing) | `MIN/MAX(period)` compare text; RFC3339 stamps with different offsets (DST) can sort out of instant order. | Out of scope; not caused by this diff. |

## Verified beyond automated tests

Not a visual change in layout: the same `<bdi dir="ltr">` cells now hold a
shorter localized date instead of a raw one. No screenshot taken; the
docs-shots seed archives no report, so no shot shows this line.

## Gate

See the PR: `gofmt -l .` clean, `go build ./...`, `go vet`, `go test
./internal/httpx/... ./internal/pages/...`, `golangci-lint run ./...`,
data-access / i18n / rtl-logical-css / core-neutral / docs-shots guards.

## Verdict

Safe to merge.
