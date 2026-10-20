# Review: Report retention card — isolate and localize the "Records held" stamps (ut-docs#3566)

- **Date:** 2026-10-20
- **Branch:** `fix/3566-retention-coverage-bidi`
- **Author:** Opus 5.5 (lane:cloud-41). **Reviewer:** Fable 5.1, fresh context.

## What shipped

`web/ui/pages/settings.html`, Settings → Report retention: the
`settings.retention.coverage_label` line printed `MIN(period)` and
`MAX(period)` from `report_archive` as raw RFC3339 strings inside RTL text.
In fa/ar they merged and showed up in the wrong order. Now each value is:

- wrapped in its own `<bdi dir="ltr">`, with `data-testid`
  `retention-coverage-earliest` / `-latest`;
- rendered with the existing `datetime` helper. It shows the locale's
  date order and digit shape plus a 24h clock in local time. A value that
  isn't a timestamp comes through verbatim.

No new locale keys and no Go changes.

## Tests (TDD)

`internal/pages/settings_retention_coverage_3566_test.go`:
- fa/ar/en: the coverage line has an LTR-isolated earliest/latest `<bdi>`
  pair, earliest first, each equal to `httpx.FormatDateTime(t, locale)`.
  No raw RFC3339 is left.
- A legacy bare-date period (`2026-08-03`) still shows verbatim and
  isolated.

Both failed before the template change ("no LTR-isolated earliest/latest
<bdi> pair") and pass after. The author watched that happen.

## Verified beyond automated tests

The fa and ar settings pages were rendered from the test harness with the
real `app.css` and loaded in Chromium at 1024×600. The earliest `<bdi>`
sits to the right of the latest one, with no overlap (fa x=302 vs 152,
ar x=305 vs 155). The fa screenshot reads, right to left: label →
`۰۳/۰۸/۲۰۲۶ ۱۸:۰۵` → `۲۱/۰۹/۲۰۲۶ ۱۹:۴۰` → count.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Review record missing | Fixed (this file) |
| 2 | minor | `MIN`/`MAX(period)` compare strings. A legacy bare-date row next to an RFC3339 row renders one raw date beside one localized stamp. This data shape was already there before this change. | Deferred: follow-up Backlog card (teach `ParseStoredTime` the `2006-01-02` layout) |
| 3 | nit | `datetime` vs `date`: the clock matters because a day can hold two closes | Accepted as is |
| 4 | nit | `dir="ltr"` vs `auto`: fa/ar digits are not strong characters, so `auto` would inherit RTL and scramble the stamp | Accepted as is (correct) |
| 5 | nit | The `{{/* */}}` comment inside `<p>` adds no visible whitespace | Accepted |
| 6 | nit | Test time zone is deterministic; the `T18:05` guard is redundant | Accepted |
| 7 | — | No e2e, docs-shots or help text asserts the old raw text | Verified |

Out of scope: the `(count)` still prints Latin digits. That is the same
class as ut-docs#4281 (on-screen number digit shape).

## Gate

`gofmt -l .` clean, `go build ./...`, `go test ./...`, `golangci-lint run
./...` (0 issues). The template-relevant guards pass: i18n, rtl-logical-css,
no-inline-handlers, compliance-claims, competitor-naming, help-topics,
help-drift, mail-addresses, core-neutral, data-access, no-showmodal,
kiosk-engine and page-http-error.

## Verdict

Safe to merge.
