# Tasks: Inventory Movements & Returns (002d)

## Phase 1: Foundations
- [ ] IR-101 Add/verify inventory aggregation helper and unit tests for sample movements.

## Phase 2: Receipts & Adjustments
- [ ] IR-201 Implement stock receipt/adjust handlers writing `stock_movements` and updating `inventory` aggregates (`internal/pos`, `internal/pages`).

## Phase 3: Negative Inventory Policy
- [ ] IR-301 Add pre-check to block negative inventory on checkout; return actionable error.
- [ ] IR-302 Implement manager override flow with audit entry (actor, reason, snapshot) and UI hook.

## Phase 4: Returns
- [ ] IR-401 Implement return flow as new sale with `sale_type='return'` linked via `sale_links`; create positive movements.
- [ ] IR-402 Ensure receipts reference original sale for returns.

## Phase 5: Low-Stock Alerts
- [ ] IR-501 Surface low-stock alerts based on reorder levels; expose in UI/API.

## Phase 6: Tests
- [ ] IR-601 Unit tests for negative-inventory checks and override audit payload.
- [ ] IR-602 Integration test for return flow inventory effects and receipt linkage.
- [ ] IR-603 Tests for low-stock flag surfacing.
