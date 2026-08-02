# Code review: persist order_type on sales and thread it to kitchen tickets

**Date:** 2026-08-02
**Scope:** `internal/db/migrations/026_sale_order_type.sql` (new),
`internal/data/pos_repo.go`, `internal/pos/sales.go`,
`internal/pages/{pos_api.go,self_order_shop.go,sync_sales.go,kitchen_print.go}`,
plus test-schema fixes in `internal/pages/ui_smoke_test.go`,
`internal/pos/{offline_resilience_test.go,sales_test.go,performance_test.go}`,
`internal/db/{barcode_seed_test.go,dead_seed_test.go}`.
**Trigger:** ut-docs#181 (rescoped this cycle — original card bundled 4
gaps; this slice covers only order_type persistence + kitchen ticket
wiring, the other two split into ut-docs#259/#260).

## What shipped

`pos.Service` already tracked order type (dine-in/takeaway) in-memory
during checkout (`Service.orderType`, set via `SetOrderType`), but
discarded it once a sale completed — a past sale's receipt/journal/
kitchen ticket could never show whether it was dine-in or takeaway.
`buildKitchenTicket`'s own comment admitted "not yet — optional fields."

- New append-only migration `026_sale_order_type.sql`:
  `ALTER TABLE sales ADD COLUMN order_type TEXT NOT NULL DEFAULT ''`.
- `POSRepo.InsertSale` gains an `orderType` param, persisted; `SaleDetail`
  gains `OrderType`; `GetSaleDetail`/`GetSaleDetailByID` read it back.
- `pos.SaleInput` gains `OrderType`; `CompleteSale` threads it to
  `InsertSale`, and **clamps** it to `""`/`OrderTypeTakeaway` (see
  independent-review finding below) before anything downstream sees it.
- Cashier checkout (`pos_api.go`) and kiosk checkout
  (`self_order_shop.go`) set `saleInput.OrderType` from
  `d.Engine.OrderType()`.
- LAN sync journal replay (`sync_sales.go`'s `applyJournal`) carries
  `OrderType` through from the journaled `SaleDetail`, same pattern as
  `SaleDiscount`/`ServiceCharge`.
- `buildKitchenTicket` sets `KitchenTicket.OrderType` from the persisted
  sale.
- Refund/return paths (`refund_page.go`, `inventory_api.go`) deliberately
  untouched — neither creates a forward sale, so order type doesn't
  apply there.
- No ADR (extends an existing tracked-but-unpersisted field, no new
  architecture). No new i18n strings (kitchen ticket text is non-i18n
  thermal-printer text, same pattern as the existing `kitchenStation`
  constant — `strings.ToUpper` at render time).

## Test-schema fallout (not a behavior change, a fixture gap)

Four `internal/pos`/`internal/pages` test files hand-roll their own
`sales` table schema instead of running real migrations; two
`internal/db` tests simulate an "upgrade from an older till" by
rewinding `schema_migrations` and replaying old migrations against a
live DB (`ALTER TABLE ADD COLUMN` isn't idempotent). Both needed the new
column added/dropped respectively — same class of fix already applied
for migrations 024/025.

## New tests

- `TestPOSRepo_InsertSale_OrderTypeRoundtrip` (`internal/data`).
- `TestBuildKitchenTicket_IncludesOrderType` (`internal/pages`).
- `TestCompleteSale_ClampsUnknownOrderTypeToDineIn` (`internal/pos`,
  added post-review — see below).

## Verification (self, before independent review)

- `go build ./... && go vet ./...`: clean.
- `go test ./...`: green except `TestSaveCleansUpDirectoryOnWriteFailure`
  (`internal/issuereport`) — pre-existing, already-filed ut-docs#258
  (fails under a root-run sandbox), confirmed unrelated (fails
  identically on an unmodified checkout).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`:
  both green.
- Fail-then-pass, done personally: reverted
  `OrderType: detail.OrderType,` in `kitchen_print.go`, ran
  `TestBuildKitchenTicket_IncludesOrderType`, confirmed it fails with
  `ticket.OrderType = "", want "takeaway"` (a real assertion failure,
  not a panic), restored, confirmed it passes again.

## Independent review

Different-model subagent (Opus), fully independent re-verification:

- Re-ran `go build`/`go vet`/both guards/`go test ./...` from scratch —
  same results as above. Independently read the sandboxed-root test to
  confirm the ut-docs#258 claim is plausible (`0o500` dir pre-created,
  root bypasses mode bits) rather than taking it on faith.
- Hand-counted the `InsertSale` SQL: 24 columns / 24 VALUES slots (22 `?`
  + 2 literals) / 22 Go args, every position mapped to the right column.
  Same for `GetSaleDetail`'s SELECT vs. `Scan` targets (16/16). No
  off-by-one — this is exactly the class of mistake caught and fixed
  once already during Dev (a missing placeholder), so the independent
  recount was warranted.
- Checked all 6 `InsertSale` call sites (prod + tests) for correct
  argument position, not just compile-clean.
- Independently reverted `buildKitchenTicket`'s wiring itself (not
  trusting the self-reported fail-then-pass), reproduced the same
  failure, restored, confirmed green.
- Confirmed migration 026 is genuinely append-only (new file, no
  existing migration edited) and the `db` seed-test rewind ranges
  correctly cover it.
- Confirmed order-type capture happens before `d.Engine.Reset()` on both
  checkout paths (no stale/blank value at capture time).
- No file I/O, no cwd-relative paths, no secrets, no real client/shop
  names in this diff.
- **Finding (fixed):** `sync_sales.go`'s journal replay path passes a
  remote peer's `OrderType` straight through unvalidated, against
  `CLAUDE.md`'s "validate all external input" rule — the cashier path
  clamps to `""`/`takeaway` via form-parsing, the journal path didn't.
  Low severity (not a regression — same class of exposure as line-name
  snapshots already had), but cheap to close. **Fixed**: `CompleteSale`
  now clamps `OrderType` to `""`/`OrderTypeTakeaway` as the one choke
  point every caller (cashier, kiosk, sync replay) goes through, with a
  new regression test (`TestCompleteSale_ClampsUnknownOrderTypeToDineIn`)
  independently fail-then-pass verified (reverted the clamp, confirmed
  the test fails with the injected garbage value persisted verbatim,
  restored, confirmed green).
- **Deferred to new Backlog cards** (not blocking this PR):
  - ut-docs#261 — dine-in prints nothing on the kitchen ticket (silent
    for `""`), and the order-type text is hardcoded English.
  - ut-docs#262 — `data.SaleDetail`/journal payload has no `json` tags,
    so LAN sync wire keys are PascalCase, not snake_case
    (`CLAUDE.md`-violating, pre-existing across the whole struct,
    `OrderType` just joins it).
  - ut-docs#263 — the `internal/db` upgrade-simulation tests' manual
    per-migration `DROP COLUMN` rewind should be made generic; this is
    the third migration (024/025/026) that needed it hand-edited.

## Verdict

**Safe to merge.** Independent review found one real, cheap-to-fix gap
(order_type validation on the sync replay path), which was fixed and
independently fail-then-pass verified. Three additional non-blocking
improvements filed as new Backlog cards rather than expanding this
diff's scope.
