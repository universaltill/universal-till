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
- Checkout blocks negative inventory unless manager override with audit payload (actor, reason, snapshot).
- Returns create new sale with `sale_type='return'` linked via `sale_links`; stock increments accordingly.
- Low-stock alerts displayed based on reorder levels.

## Acceptance Criteria
- Stock receipt/adjust flows update inventory correctly; tests cover aggregation.
- Negative stock blocked by default; override path audited and tested.
- Return flow updates stock and links to original sale; receipts reflect linkage.
- Low-stock flags surface in UI/API from reorder levels.
