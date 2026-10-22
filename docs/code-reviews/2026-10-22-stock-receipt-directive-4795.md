# Review — adjust_stock receipt type and cost price, till side (ut-docs#4795)

**Date:** 2026-10-22 · **Built by:** Sonnet 5.5 (Dev lane, local) · **Branch:** `feat/4795-stock-receipt-fields` · **Independent review:** DONE 2026-10-22 (Opus, different model); findings and handling below.

Companions: ut-cloud and ut-my-shop branches of the same name. Release: the cloud gates `type=receive` / `cost_price` on a main till at **0.34.16**, the next tag after v0.34.15 — this change must be what v0.34.16 ships.

## What shipped here
- `cloudsync.apply` (`adjust_stock`): reads `type` (absent, `""` or `adjust` stay on `AdjustStock`; `receive` goes to the new `ReceiveStock` hook; any other value, or a non-text one, fails `unsupported adjust_stock type`) and `cost_price` (receive only, whole minor units 0..`MaxReceiveCostMinor` = the till's item price ceiling; otherwise fails `invalid cost_price`). A negative receipt fails; a till with no `ReceiveStock` hook fails with `stock receipts are not supported on this till` instead of booking an adjustment.
- `pages.cloudReceiveStock` and `cloudAdjustStock` both go through one `cloudRecordStock`: the same `resolveStockSlot` checks, `pos.RecordStockMovement` (the function POST /api/inventory/receipt uses, so the same `stock_movements` row with `cost_price`, `inventory` update and `audit_log` row), `publishStockAdjusted` with `stockMovementReason(type)`, and the link nudge. Nothing duplicated; the adjustment result text and behaviour are unchanged.
- Roles: unchanged. Directives run as the `system` actor as before (reason prefixed `cloud: `); the receipt endpoint's `stock_management` check guards the till's own form, which is not involved.

## Parity
`TestStockReceiptRequestFieldsArePinned` pins `StockReceiptRequest`'s fields and the cost ceiling; ut-cloud's `internal/stockreceiptcontract` reads the same struct from source in `manifest-contract-guard`.

## Not done
- No till help change: the till's Inventory help does not describe cloud-originated movements. Movements from my. now show as `receive` (with the cost) in the audit trail like a local receipt.

## Tests
`cloudsync/receive_stock_4795_test.go` (routing, bad type/cost, no hook), `pages/cloud_receive_stock_4795_test.go` (movement row type/cost/NULL for none, audit row, refusals share the adjustment's, field pin). `go test ./internal/cloudsync ./internal/pages` green.

## Independent review (Opus, 2026-10-22): findings and handling
Done: an independent, different-model (Opus) review read all three branches. Eight findings; each was handled as below (code fixes landed in ut-cloud and ut-my-shop in the follow-up commit; universal-till needed no change).

| # | Finding | Handling |
|---|---------|----------|
| 1 | The version gate (`ReceiveStockMinTillVersion = "0.34.16"`) is a guess at the next tag | **Merge order, not code.** Merge the till PR first, confirm the release tag contains its commit, then merge ut-cloud with the matching constant (change the constant if the tag differs). |
| 2 | A retried directive is applied twice (a receipt would book twice) | Pre-existing in the directive path for every `adjust_stock`; **follow-up card**, not part of this change. |
| 3 | my.: the *waiting* chip sits under the form's location while the table shows only the picker's, so choosing another location closes the form with no visible feedback and the change can be entered twice | **Fixed (ut-my-shop).** A queue for a location other than the picker's shows a status line naming it (`manage_app.inventory.queued_elsewhere`, all 10 locales + `catalogStrings` fallback); the line goes when the picker changes. Test added. |
| 4 | The contract test pinned the till's HTTP form struct, which the directive path does not use | **Fixed (ut-cloud).** `stockreceiptcontract` now also parses the till's `cloudsync.go` `adjust_stock` case and compares the payload keys it reads (`type`, `cost_price`, `qty_delta`, …) with the keys the cloud endpoint writes, and `MaxReceiveCostMinor` with `MaxStockCostMinor`; unchecked type assertions replaced by `t.Fatalf` checks. Mutation-checked on a scratch copy of the till file (renamed key, changed constant, renamed `fnum` key: each fails). |
| 5 | `AdjustStockNeedsReceiveGate` is unused outside tests | Harmless: the admin directive form does not forward `type` / `cost_price`, so no path needs it. Left as the predicate the gate's tests pin. |
| 6 | my.: the cost example `1.49` is wrong for zero-decimal currencies, and an unreported `currency_decimals` (-1) made `parseMinor` read `149` as 14900 | **Fixed (ut-my-shop).** The example follows `currency_decimals` (0 gives `149`); unreported hides the example and a typed cost is refused with `manage_app.inventory.cost_unknown` (`parseCost` returns null for decimals below 0); an empty cost still sends. Hint split into `cost_hint` + `cost_example`. Tests added. |
| 7 | Help claimed the till records the receipt "exactly as if entered on the till" | **Fixed (ut-my-shop help en/de/tr).** Now: the till records the movement (type, cost, reason prefixed `cloud: `) in its stock movements, attributed to the cloud rather than a till user. |
| 8 | The admin directive list showed a receipt like an adjustment | **Fixed (ut-cloud).** `directiveSummary` shows `i-tea +12 (receipt)` (same unlocalised style as the `count` label, so no new i18n). Test added. |
