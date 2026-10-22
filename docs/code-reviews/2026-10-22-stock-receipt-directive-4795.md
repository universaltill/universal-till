# Review — adjust_stock receipt type and cost price, till side (ut-docs#4795)

**Date:** 2026-10-22 · **Built by:** Sonnet 5.5 (Dev lane, local) · **Branch:** `feat/4795-stock-receipt-fields` · **Independent review:** NOT done in the Dev pass; the Reviewer role still owes one (different model) before merge.

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
