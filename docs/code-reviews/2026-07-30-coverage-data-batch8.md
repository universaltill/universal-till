# Code review — coverage batch 8: `internal/data/pos_repo.go` (+2 real fixes)

- **Date**: 2026-07-30
- **Branch**: `test/coverage-data-batch8-pos-repo`
- **Scope**: the 42 zero-coverage functions in `internal/data/pos_repo.go`
  (per batch 7's closing note), plus two real product bugs the new tests
  exposed, fixed TDD-style in the same batch.
- **Author lanes**: four parallel same-session dev agents, one domain
  group each (reports / inventory / sales lifecycle / lookups-ensure-
  payments-pricing), each owning exactly one new test file; orchestrator
  wrote the fixes + regression tests.
- **Independent review**: different-model (opus) subagent, findings below.

## What shipped

Five new test files in `internal/data/`:

- `pos_repo_batch8_reports_test.go` — SalesByDay, SlowItems, DeadStock,
  TaxSummary, MarginByItem, DayTotal, SeasonalUpcoming, SalesByWeekday /
  SalesByHour / busyBuckets, ItemDailySellRates (90–100% each).
- `pos_repo_batch8_inventory_test.go` — AggregateInventory,
  CheckNegativeInventory, RecordNegativeInventoryOverride,
  GetLowStockItems, ListStockLocations, ListStockLevels, CurrentQty
  (77–92% each).
- `pos_repo_batch8_sales_test.go` — LookupUserRole, FindSaleIDByReceipt,
  ListSaleLineSnapshots, GetReceiptNo, NextReceiptNo (incl. per-till
  `sync.receipt_prefix` namespacing), InsertSale/InsertSaleLine roundtrip,
  UpdateSaleStatus, RecordPaymentFailure, SaleTotals, SaleCompletedAt,
  GetSaleDetailByID, ListRecentSales (80–100% each).
- `pos_repo_batch8_lookups_test.go` — LookupCustomer, FindActivePromo,
  EnsurePaymentMethod/EnsureRegister/EnsureUser idempotency,
  ListActivePluginVersions, ListActivePaymentMethods,
  **ListActiveNonCashPaymentMethods (the kiosk's cash-exclusion filter,
  proven to exclude `type='cash'` and inactive methods)**,
  AppendPriceHistoryItem/Variant close/open invariants, valueOrDefault
  (77–100% each).
- `pos_repo_batch8_fixes_test.go` — failing-first regression tests for
  the two production fixes below.

Package coverage: **63.6% → 76.2%** of statements. Every covered function
was checked for live production callers first (all 42 have them; the two
private helpers are covered via their exported callers) — no dead-surface
coverage theater.

## Two real bugs found by writing these tests, fixed here (TDD)

Both confirmed failing-first with the exact predicted errors, then fixed,
then passing; production diff is 2 lines total.

1. **`GetLowStockItems` — one never-stocked item silently killed ALL
   low-stock alerts.** `inv.location_id` came out of a LEFT JOIN
   un-COALESCEd and was scanned into a plain `string`, so any item with
   `reorder_level > 0` but no inventory row yet (a brand-new item before
   first goods-receipt) made the whole query error — and the back-office
   caller (`internal/pages/backoffice_page.go:39`) drops that error, so
   the owner just saw no low-stock alerts at all. Fix:
   `COALESCE(inv.location_id, '')`; never-stocked items now correctly
   appear as qty 0. Known accepted limit: with a location filter the
   `AND inv.location_id = ?` clause still excludes never-stocked items.
2. **`MarginByItem` — variant sales vanished from the margin report
   unless the variant had its own `cost_price`.** The doc comment says
   "using the variant's cost when present, else the item's", but items
   were joined on `sl.item_id`, which is NULL for variant lines (schema
   CHECK: exactly one of item_id/variant_id) — so the item-cost fallback
   could never fire. Fix: join items via
   `COALESCE(NULLIF(sl.item_id,''), v.item_id)`, the same resolution
   pattern `SeasonalUpcoming` already uses.

## Suspected bugs found but deliberately NOT fixed here (queued instead)

- `SaleCompletedAt` scans NULL `completed_at` into a plain string →
  error; live caller `pos_api.go:616` swallows it and proceeds with a
  zero time in the refund-window check. Low severity today (InsertSale
  always writes completed_at); pinned in tests.
- `UpdateSaleStatus` is a silent no-op for an unknown sale id (no
  RowsAffected check). Pinned in tests.
- `SalesByDay` has no `sale_type` filter, so completed returns count as
  positive daily revenue on the owner dashboard — inconsistent with
  TaxSummary (negates) and DayTotal/busyBuckets (exclude). Needs a
  product-semantics call, left un-asserted.

## Verification (pipeline side, performed personally by orchestrator)

- Both regression tests seen failing pre-fix with the exact predicted
  errors, passing post-fix.
- **5 mutation probes, all killed**: low-stock `<`→`<=` (boundary),
  kiosk non-cash filter removed (security-relevant), receipt-no `+1`→`+2`
  (sequencing), TaxSummary return-negation removed (money math),
  negative-inventory `<`→`<=` (sell-exactly-to-zero). Each failed the
  right test; each reverted.
- `go build ./...`, `go vet ./...`, full `go test ./...` green;
  `guard-data-access.sh` and `guard-i18n.sh` pass.

## Accepted remainder (said out loud, not silent)

Driver/scan-failure branches (`rows.Scan`/`rows.Err`/`QueryContext` on a
healthy migrated sqlite DB), interior transaction-failure branches in
AppendPriceHistory*/RecordStockMovement, and `FindActivePromo`'s
defensive `pType == ""` branch (unreachable: NOT NULL + CHECK constraint)
— all would need fault injection disproportionate to their risk; the
closed-DB error path IS covered for the report/lookup groups.

## Independent (opus) review findings

**No blockers.** The reviewer verified all five author claims
independently: per-function coverage diff of two profiles confirmed
exactly 42 `pos_repo.go` functions went 0% → covered (none remain at 0%);
both fixes proven correct and minimal (both join keys are primary keys so
the re-order cannot fan out; `inventory.location_id` is `NOT NULL` so the
COALESCE can only ever represent "no inventory row"); isolation confirmed
(per-test temp DBs, two `-shuffle=on` full-package runs green, no
`t.Parallel()`, no rolling-window time bombs). It ran **17 fresh mutation
probes** (different sites from the pipeline's five): 13 killed, including
DeadStock value math, EnsurePaymentMethod `OR IGNORE`→`OR REPLACE`,
FindActivePromo expiry guard, SaleTotals column swap, price-history
close-previous UPDATE, and both production fixes re-reverted.

**Findings fixed before commit (re-verified personally, mutations re-run
and now killed):**

1. *should-fix*: the weekday/hour bucket test derived expectations from
   Go's `tm.Local()`, making it timezone-adaptive but timezone-blind —
   under CI's `TZ=UTC` it couldn't catch a `%w`→`%u` weekday swap or
   SQLite-vs-Go local-clock drift. Now derives expected slots from a
   SQLite `strftime(...,'localtime')` control query plus a Go-local
   sanity cross-check. Honest residual, documented in the test: a
   production query that *dropped* `'localtime'` is behaviorally
   invisible under `TZ=UTC` — only catchable on non-UTC machines.
2. *should-fix*: `SeasonalUpcoming`'s Ceil rounding was untested (all
   fixtures whole-numbered; reviewer's Floor mutation survived). Added a
   weighed-stock case (2.5 sold, 1 on hand → suggest 2). Mutation re-run:
   killed.
3. *nit*: the `days<=0 → 28` default wasn't pinned (a 14-day default
   survived). Added a −340d sale visible only under a 28-day window.
   Mutation re-run: killed.
4. *nit*: a dead assertion in the stock-levels test checked for the
   variant id leaking, but a leaked variant inventory row would surface
   under its PARENT item id — assertion now targets the parent.
5. *nit*: misleading failure label in the EnsureUser idempotency test;
   fixed.
6. *nit*: a test comment claimed the UpdateSaleStatus unknown-id no-op
   was "documented" — it isn't; reworded to "pins current behavior" with
   a pointer at the QUEUE follow-up.

**Reviewer findings accepted as-is (not fixed here, queued):**

- `DeadStock`'s "has sold" subquery counts completed *returns* as sales,
  and `TopItems` lacks the `sale_type='sale'` filter `SlowItems` has —
  same family as the deferred `SalesByDay` returns issue; one QUEUE item.
- Location-filtered `GetLowStockItems` still can't show never-stocked
  items (`AND inv.location_id = ?` never matches a missed LEFT JOIN).
  Not a regression — the filtered path never hit the NULL-scan bug — but
  the reviewer rates it worth fixing (`OR inv.location_id IS NULL` is
  safe given the column is `NOT NULL`); queued.
- Fix (b) **changes reported business numbers, deliberately**: variant
  lines without their own cost now enter the margin report at the parent
  item's cost. For a premium variant this can understate cost/overstate
  margin vs. excluding the line entirely — but it matches the function's
  own doc-comment intent and the resolution pattern of
  `SeasonalUpcoming`/`ItemDailySellRates`. Called out here because
  `reports_page.go:25` drops the error, so the owner sees the new
  numbers with no other signal.
- Batch-8 test helper sprawl (four near-identical DB openers across the
  four files) — consolidate in batch 9.
- Some assertions pin exact migration-seed contents (kiosk user id,
  3 seeded stock locations); they fail loudly, accepted.

**Reviewer's ranking of the three deferred bugs** (adopted into the QUEUE
follow-up): `SalesByDay` returns-as-revenue is the one to fix next — it
feeds the reports page, back office AND the self-hosted AI Q&A
(`ask_api.go:45`), and disagrees with `TaxSummary` on the same page.
`UpdateSaleStatus` no-op is slightly worse than cosmetic (a void of an
unknown sale id returns 204 AND writes a "voided" audit row for a sale
that was never voided — audit-log poisoning; adjacent gap: `pos_api.go:668`
passes an empty actorID, so voids record no operator). `SaleCompletedAt`
NULL-scan is effectively unreachable in the field (the only production
insert always writes `completed_at`).

## Final gate (after review fixes)

`go build ./...`, `go vet ./...`, full `go test ./...` all green;
both CI guards pass; `git diff internal/data/pos_repo.go` = exactly the
two intended 2-line hunks.
