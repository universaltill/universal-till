# Code review — `stock.adjusted` event (ADR-0014, rollout step 3)

Branch: `feat/stock-adjusted-event`
Date: 2026-07-16

## Goal
Publish a `stock.adjusted` event on the plugin EventBus whenever inventory
changes, so ERP/inventory connectors (SAP, Dynamics/LS Central, generic
webhook) can mirror stock levels. Best-effort and NON-BLOCKING — it must never
delay or fail a sale, refund, or adjustment (offline-first).

## Contract (payload shape)
`plugins.StockAdjustedEvent` in `internal/plugins/ipc.go`, snake_case JSON:

| field         | json           | type      | notes                                            |
|---------------|----------------|-----------|--------------------------------------------------|
| ItemID        | `item_id`      | string    |                                                  |
| VariantID     | `variant_id`   | string    |                                                  |
| SKU           | `sku`          | string    |                                                  |
| DeltaQty      | `delta_qty`    | float64   | signed change; decimal for weighed goods         |
| NewQty        | `new_qty`      | float64   | `omitempty`; only when readily available         |
| Reason        | `reason`       | string    | `sale` \| `refund` \| `adjustment` \| `received` |
| Location      | `location`     | string    |                                                  |
| AdjustedAt    | `adjusted_at`  | time.Time | ISO-8601                                         |

Helper `EventBus.PublishStockAdjusted` mirrors `PublishSaleCompleted`. The event
mode is registered `NonBlocking` alongside `sale.completed`.

## Where it is published (all pages layer, best-effort/non-blocking)
The plugin bus is only touched from the pages layer (the same boundary as
`publishSaleCompleted`); the `pos`/`data` layers stay plugin-free. Two pages
helpers were added in `internal/pages/pos_api.go`:

- `publishStockAdjusted(ctx, d, ev)` — publishes one event; swallows errors,
  skips zero deltas, stamps `AdjustedAt`.
- `publishStockAdjustedForSale(ctx, d, in)` — one event per `SaleInput` line,
  mirroring the signed stock movement `CompleteSale` writes (sale → negative
  delta, `reason=sale`; return → positive delta, `reason=refund`). Location/SKU
  come straight from the `SaleInput` line.

Call sites (each fires *after* the underlying operation has succeeded):

| Path                              | File / function                                  | Reason        |
|-----------------------------------|--------------------------------------------------|---------------|
| Sale tender                       | `pos_api.go` (after `publishSaleCompleted`)      | `sale`        |
| Refund (UI)                       | `refund_page.go` refund handler                  | `refund`      |
| Return (inventory API)            | `inventory_api.go` `CreateReturn`                | `refund`      |
| Manual receive / adjust           | `inventory_api.go` `CreateStockReceipt`          | `received`/`adjustment` |
| Catalog import (opening stock)    | `import_page.go` commit path                     | `received`    |
| Offline sale replay (primary)     | `sync_sales.go` `applyJournal`                   | `sale`/`refund` |

Notes:
- `sync_sales.applyJournal` publishes only after its `SaleExists` idempotency
  guard, so a replayed sale emits at most once per sale id on this node.
- `new_qty` is left unset everywhere for now: it is not returned by
  `RecordStockMovement`/`CompleteSale`, and computing it would add a synchronous
  query to the tender path — deliberately avoided per offline-first. The field
  exists (with `omitempty`) so connectors can rely on the shape when we later
  populate it.

## Why not publish in `internal/data.RecordStockMovement` (the true choke point)
It is the single point every movement passes through, but (a) the data layer
must not import `plugins` (import cycle + layering), and (b) it runs *inside*
the sale transaction — publishing there would emit phantom events on rollback
and couple best-effort side effects to commit. Publishing at the pages layer
after success matches the established `publishSaleCompleted` pattern.

## Tests
- New contract test `internal/plugins/stock_event_contract_test.go`
  (`TestStockAdjustedEvent_ConnectorContract`), modelled on the sale test:
  installs a connector manifest with a `stock.adjusted` hook + `events:receive`,
  subscribes, publishes, and asserts item/variant/SKU, the signed decimal delta,
  reason, location, and new_qty survive the round-trip.

## Verification
- `go build ./...` — clean
- `go test ./...` — green (incl. new test; `go vet` clean)
- `bash scripts/ci/guard-data-access.sh` — pass (no inline SQL added outside data/db)
