# Review — cleared baskets on the Z report (ut-docs#1122)

**Date:** 2026-10-21 · **Lane:** lane:cloud-41 · **Branch:** `feat/1122-cleared-baskets`
**Author model:** Opus 5.5 (dev subagent) · **Reviewer model:** Fable 5.1 (independent subagent, separate worktree)

## What shipped

- A live cashier basket with at least one priced or add-by-hand line that
  **New sale** (`/api/pos/reset`) clears without tender and without the
  resumed-order re-park (#3582) now writes one `cleared_baskets` row
  (`internal/pages/cleared_basket.go`, `internal/data/cleared_basket_repo.go`).
  The write is best-effort: local SQLite, logged on failure, never blocks
  or fails the reset (ADR-0003).
- Migration `086_cleared_baskets.sql`, modelled on 064 `no_sale_events`:
  - `local_date` = `date(created_at,'localtime')` (InsertSale's rule) and an
    index on it;
  - `cleared_baskets_archive` reset twin, registered in `resetArchiveTables`
    / `restoreEmptyCheckTables`;
  - both tables classified in `sync_admin_repo.go`;
  - checksum pinned.
- `data.EODReport.ClearedBasketCount` / `ClearedBasketTotal` are filled in
  `dateRangeSummary` and `dateRangeSummaryInstant`. They are never folded
  into Gross, Net, Refunds or Stornos.
- The printed Z report gets its own **CLEARED BEFORE PAYMENT** section on a
  day with at least one cleared basket and none otherwise (the
  STORNOS/GUTSCHEINE convention).
- Keys `reports.zpaper.cleared_title` / `cleared_baskets` exist in
  en/tr/fa/ar with `en.meta.json` metadata. A help paragraph is in
  `web/help/{en,de,tr,fa,ar}/reports.md`.
- `pos.Service.ResetAndSnapshot()` reads and clears the basket under one
  lock. The handler uses it instead of `Snapshot()` followed by `Reset()`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A replica's cleared baskets stay on the replica. The main till's Z report (which aggregates replica sales and no-sales via the D3 journal, #3562) doesn't include them. | **Accepted:** follow-up ut-docs#4711. Same staging as no-sales (#2558 → #3562). |
| 2 | minor | A German till prints the English heading until `ut-plugin-language-de` carries the keys. | **Fixed in the same cycle.** Pack PRs for de/es/pt follow the core merge. |
| 3 | minor | `Snapshot()` then `Reset()` took the lock twice. A scan in between would be wiped and not counted (an undercount only). | **Fixed:** `ResetAndSnapshot()` + `TestResetAndSnapshot_ReturnsTheClearedBasket`. |
| 4 | nit | fa help: "سفارش بازی" read as "game order". | **Fixed:** "یک سفارش باز". |
| 5 | nit | tr `cleared_baskets` was 21+ characters against `max_length` 20. | **Fixed:** "Boşaltılan (%d)". |
| 6 | nit | A by-hand-only basket records total 0. | **Accepted:** by-hand lines carry no price, and the count is the signal. |

## Verified

The reviewer ran these:
- `go build ./...` and `go vet ./internal/...`;
- `go test ./internal/data/... ./internal/pages/... ./internal/db/...`;
- the guards i18n, i18n-meta, help-drift and data-access.

The reviewer then **re-verified the TDD claims** by reverting each piece of
production code, seeing its test fail with a meaningful message, and
restoring it:
- the reset hook → `TestResetHandler_ClearedBasketIsRecorded` / `_ByHandOnly…`;
- the empty-basket guard → `…_EmptyBasketRecordsNoClearedBasket`;
- the footer block → `TestEODPrint_ClearedBasketsSection{WhenNonZero,Translated}`;
- the EOD scans → `TestEndOfDay_ClearedBasketsCountedPerDayRangeAndInstant`;
- each archive registration → `TestResetThenRestoreRoundTrip_ClearedBaskets`.

All four `/api/pos/reset` callers in `web/ui/pages/index.html` are
operator-pressed New sale buttons. Tender resets the engine itself
(pos_api.go `completeTender`), so a New sale press after a paid sale sees
an empty basket and writes nothing: no double count.

The orchestrator re-ran the following after the fixes:
- gofmt, `go build ./...`, golangci-lint (0 issues);
- the pos and pages tests and the i18n/help guards;
- the full `go test ./...`.

Not driven in a browser: the change is a server-side record plus a
printed-paper line, covered by handler and print-layout tests (width,
Windows-1254 for tr).

## Verdict

Safe to merge.

## Deferred

- Journal a replica's cleared baskets to the main till (ut-docs#4711, as #3562 did for no-sales).
- Cloud sales aggregate (ADR-0111) has no cleared-basket figure. That is out of scope; it goes on ut-docs#4711.
