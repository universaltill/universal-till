# Review: channel-order Z compare ignores GeneratedAt (ut-docs#5137)

## What shipped

Test-only change in `internal/pages/cloud_channel_order_4768_test.go`.
Production code is unchanged.

- **Cause.** `TestCloudApplyChannelOrder_LeavesZReportDrawerAndFiscalJournalUnchanged`
  `reflect.DeepEqual`s two `data.EODReport` values from
  `POSRepo.EndOfDayInstant`, taken before and after `cloudApplyChannelOrder`.
  `EODReport.GeneratedAt` is `time.Now()` at second resolution
  (`internal/data/pos_repo.go`). When the apply crossed a second boundary
  the reports differed only there (`19:14:46Z` vs `19:14:47Z` in
  `pages-shuffle (seed 22)` on universal-till#2208), and the test failed.
- **Fix.** Blank `GeneratedAt` on both reports before the compare. Every
  counted field (counts, totals, receipt range, breakdowns, cash
  reconciliation) is still compared.
- The after-calls of `EndOfDayInstant` and `ListRecentSales` now fail the
  test on error instead of discarding it (an error used to leave a zero
  value and a misleading "changed" message).

## Review

Built by Sonnet. Independent review by Opus 5.5 in a separate worktree.

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | `GeneratedAt` is the only wall-clock field `EndOfDayInstant` sets; the rest comes from SQL over stored rows. `EODReport` has no maps. | info | Confirmed |
| 2 | `Methods`/`Tills` queries can tie on their `ORDER BY`, leaving row order open in theory. Here there are no payments and one till, so they are nil or one row. | low | Accepted, cannot flake in this test |
| 3 | The journal compare has no wall-clock field (`SaleJournalEntry.CreatedAt` is the stored `sales.created_at`). | info | Confirmed |
| 4 | `journalAfter, _ :=` discarded its error. | low | Fixed |
| 5 | No other test DeepEquals a whole `EODReport`; the others compare `TaxBands` only or use a fixed `GeneratedAt`. | info | Nothing else to fix |

## Verified

- TDD, re-run by the orchestrator in a throwaway worktree: without the
  blanking line and with `zAfter.GeneratedAt` forced to another second,
  the test fails ("Z report changed"); with the fix, it passes.
- Dev proved the test is not weakened: `zAfter.SalesCount++` still fails it.
- `-count=300` of the test (reviewer) and `-count=20` (dev) pass; full
  `go test ./internal/pages/ -count=1` passes; `gofmt`, `go vet`,
  `go build ./...`, `golangci-lint` (pages) clean; CI guards run before
  the PR.

**Verdict:** safe to merge. Nothing deferred.
