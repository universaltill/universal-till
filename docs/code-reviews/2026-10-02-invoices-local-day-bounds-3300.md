# Review: invoice register filters by local days (ut-docs#3300)

- **Date:** 2026-10-02
- **Branch:** `fix/3300-invoices-local-day-bounds`
- **Author:** lane:cloud-54 (Opus 5.5). **Reviewer:** Fable (independent subagent, separate worktree).

## What shipped
- `internal/pages/invoice_page.go`: new `invoiceUTCBounds`. It converts the
  register's `from`/`to` (local `type="date"` values) to the UTC RFC3339
  bounds `invoices.issued_at` is stored in:
  - `from` = the instant of local midnight;
  - `to` = the last second before the *next* local midnight (`AddDate`, so DST days are correct).

  `GET /invoices` (list + totals) and `GET /api/invoices/export` both use
  it. The bare-load default `from` (start of this local month) goes through
  the same conversion. Before, east of UTC, the first local hour(s) of a
  month were hidden, and explicit ranges were off by the UTC offset.
- Clock and zone are injectable (`invoiceNow`, `invoiceLoc`). TZ set inside
  a test never moved `time.Local`, so the old Honolulu test wasn't
  exercising the zone it claimed. It is now pinned through `invoiceLoc`.
- `web/ui/pages/invoices.html`: the Issued column uses `date` (local), not
  `dateUTC`, so a row's date matches the local day it is filtered under.
  `dateUTC`'s comment in `internal/httpx/httpx.go` was updated.
- The docs-shots surface hash was refreshed. No rendered pixel changes,
  because docs-shots render in UTC, where local = UTC.

## Tests (TDD)
New tests, in `internal/pages/invoice_page_test.go`:
- `TestGetInvoices_DefaultShowsInvoiceIssuedAfterLocalMidnightOnTheFirst`: UTC+1, 00:30 on 1 Oct.
- `TestGetInvoices_ExplicitRangeIsLocalDays`: UTC+3; checks both day edges in the list, the totals and the CSV.
- `TestGetInvoices_IssuedColumnShowsLocalDate`
- `TestInvoiceUTCBounds`: east, west, open ends, pass-through, and the Europe/London DST day.

All of them failed with their assertion messages while `invoiceUTCBounds`
was a pass-through (or `dateUTC` was restored) and pass with the fix. The
reviewer re-verified this independently in its own worktree.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Issued column still showed the UTC date, which contradicted the new local-day bucket (UTC+1, 00:20 on 1 Oct listed under October but shown as 30.09) | **Fixed**: `date` in template + regression test |
| 2 | minor | Bounds assume issued_at is second-precision `Z`. That holds for the only writer (`invoice_page.go` issueInvoice); no sync path writes `invoices`. A fractional-second writer would sort before the `from` bound | Accepted; no such writer exists |
| 3 | nit | `reportNow()`'s +1s pad was pointless with an open `to`, and flips `from` to next month in the month's last second | **Fixed**: `invoiceNow = time.Now` |
| 4 | nit | Help topic could say "local days" | Accepted: `web/help/en/invoices.md` already describes the (now correct) calendar-month behaviour |
| 5 | nit | Stale pointer in `dateUTC` comment | **Fixed** |

Other checks: SQL stays in `internal/data`, no money/i18n changes, no file
writes, test names are synthetic, and nothing in `internal/pages` uses
`t.Parallel` (so the package-var overrides are safe). The only callers of
`InvoiceRepo.List`/`Totals` are the two converted handlers.

## Gate
`gofmt`, `go build ./...`, `go vet`, `go test ./... -race`,
`golangci-lint` (pages), and the CI guards. Locally,
`guard-deadcode-baseline` fails on `internal/logging/file.go` (desktop root
skipped without GTK headers), and `internal/netreach` `TestPanickingProbeStillClearsInFlight` has a data race under `-race` that also fails on `origin/main` (filed as a Backlog card). Both are pre-existing and not touched here. In this container, `-race` on `internal/{db,pages,plugins}` hit the 600s timeout; `internal/pages` passes without `-race` (181s), and CI runs the full suite.

## Verdict
Safe to merge once CI is green.
