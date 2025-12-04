# Inventory Movements & Returns (002d)

Status: Draft
Principles: offline-first; integer money; no schema changes

## Purpose & Goals
- Implement stock receipts/adjustments and inventory aggregation per stock_location.
- Enforce negative-inventory prevention with manager override + audit.
- Model returns as sales with sale_links and positive stock movements.
- Surface low-stock alerts using reorder levels.

## Scope
- Stock movement writes/reads; inventory aggregates.
- Return flow creating linked sales and reversing quantities.
- Override policy and audit logging.
- Low-stock alert flagging (no automation beyond surfacing).

## Non-Goals
- Catalog CRUD, payment capture, plugin behavior (covered elsewhere).

## Functional Requirements
- Stock receipts/adjustments create `stock_movements` rows and update `inventory` aggregates.
- Checkout blocks negative inventory unless manager override with audit payload:
  - **Manager role**: `users.role IN ('manager', 'admin')`
  - **Audit payload**: `{"actor_id": "...", "reason": "...", "snapshot": {"item_id": "...", "location_id": "...", "qty_before": N}}`
- Returns create new sale with `sale_type='return'` linked via `sale_links`; stock increments accordingly.
- Low-stock alerts calculated using `items.reorder_level` (integer threshold); displayed when `inventory.quantity < reorder_level`.

## Acceptance Criteria
- Stock receipt/adjust flows update inventory correctly; tests cover aggregation.
- Negative stock blocked by default; override path requires manager role, captures JSON snapshot, audited and tested.
- Return flow updates stock and links to original sale via `sale_links`; receipts display original receipt_no.
- Low-stock flags surface in UI/API when `inventory.quantity < items.reorder_level` (`reorder_level` column present in `001_init.sql`).
