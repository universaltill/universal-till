# Test coverage batch 2: pos_repo shift/refund/EOD/audit lifecycle

2026-07-29

Second batch of the near-100%-coverage backfill. `internal/data/pos_repo.go`
is the single biggest gap in the codebase (~150 functions, the core sales/
shift/inventory/refund business logic) — tackling it in focused sub-batches
by risk area rather than one pass, since it's too large for one sitting.

This batch: shift lifecycle reads (`GetShiftOpeningCash`, `CurrentOpenShift`,
`ListRecentShifts`, `ListRegisters`), the refund/return chain
(`OriginalSaleIDFor`, `RefundLineKey`, `ReturnedQuantities` — the
double-refund guard's data source, `OriginalReceiptFor`, `ReturnReceiptsFor`,
`ReceiptExists`), low-level sale-graph writes (`InsertSaleDiscount`,
`InsertSaleLink`, `UpsertInventory`, `SetSaleProvenance`), sync/replica
helpers (`LocalSalesSince`, `CountLocalSalesSince`, `SaleExists`), EOD/
reporting (`EndOfDay`, `ArchiveReport`, `ListArchivedReports`,
`HasArchivedReport`), `ListPaymentMethodIDs`, and `AuditActionSummary` (the
"Ask your till" shrinkage-signal query).

## What changed

- `internal/data/pos_repo_lifecycle_test.go` (new). Opens the DB via the
  real migrations (`internal/db.Open`) rather than a hand-rolled schema —
  this batch touches enough real tables with real foreign keys (shifts,
  report_archive, sale_links, registers) that a minimal fixture would just
  be duplicating the migration. The helper clears migration-seeded demo
  data and re-seeds a minimal register/user/stock-location fixture to
  satisfy FK constraints.

## Bug found and fixed during writing (test-only, not production)

`TestAuditActionSummary`'s day-window filter (`datetime('now', '-N days')`)
is relative to the real wall clock. First draft used hardcoded literal
dates like `"2026-01-01"` assuming that was "now" — it isn't; the actual
system clock is 2026-07-29. Switched to `time.Now()`-relative offsets so
the test doesn't silently rot as real time passes.

## Independent review (opus)

Verified against the production code line-by-line for the four
highest-risk assertions: shift variance sign/formula, EndOfDay's sale/
return split and same-day filtering, the `RefundLineKey` composition and
`ReturnedQuantities` SUM (the actual double-refund-guard data path),
and `UpsertInventory`'s update-then-insert-on-miss fallback (confirmed
both branches are genuinely exercised). No blocking or worth-fixing
findings. Confirmed test isolation holds under `-shuffle=on -count=3`.

## Verification

`go build ./...`, `go test ./...`, `go test ./internal/data/... -count=3
-shuffle=on`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.

## Coverage delta

`internal/data/pos_repo.go`: 22 more functions moved from 0% to covered
(GetShiftOpeningCash, CurrentOpenShift, ListRecentShifts, ListRegisters,
InsertSaleDiscount, InsertSaleLink, UpsertInventory, ListPaymentMethodIDs,
OriginalSaleIDFor, ReturnedQuantities, OriginalReceiptFor,
ReturnReceiptsFor, ReceiptExists, SaleExists, SetSaleProvenance,
LocalSalesSince, CountLocalSalesSince, ArchiveReport, ListArchivedReports,
HasArchivedReport, EndOfDay, AuditActionSummary).

## Remaining in pos_repo.go (~60 functions, next sub-batches)

Highest priority next: the checkout resolution chain (`ResolveShortcutLine`,
`resolveVariant`, `resolveItem`, `resolveShortcut`, `resolveSKU`,
`resolveNameLike`, `resolvePrice`, `toShortcutLine`, `ResolveCurrentPrice`,
`lookupPriceHistory` — exercised on every single ring-up) and the rest of
shift open/close (`FindOpenShiftForRegister`, `InsertShift`,
`LoadShiftForClose`, `UpdateShiftClose`, `ShiftOpenExists`,
`SumCashPaymentsForShift`, `SumShiftAdjustments`, `ComputeExpectedCash`).
Then: `CheckNegativeInventory`/`RecordNegativeInventoryOverride`/
`GetLowStockItems`, `Ensure*` helpers, `SaleTotals`/`SaleCompletedAt`/
`GetSaleDetailByID`, and the reporting/analytics functions (`TopItems`,
`PeriodComparison`, `TaxSummary`, `MarginByItem`, `DayTotal`,
`SeasonalUpcoming`, `PaymentBreakdown`, `DeadStock`, `SlowItems`) — lower
business risk (read-only reports), tackled last within this file.
