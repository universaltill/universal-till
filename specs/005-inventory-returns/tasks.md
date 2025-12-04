# Tasks: Inventory Movements & Returns (002d)

## Phase 1: Foundations
- [X] IR-101 Add/verify inventory aggregation helper and unit tests for sample movements.

## Phase 2: Receipts & Adjustments
- [X] IR-201 Implement stock receipt/adjust handlers writing `stock_movements` and updating `inventory` aggregates (`internal/pos`, `internal/pages`).
- [X] IR-202 Build stock receipt/adjustment UI form wired to IR-201 handler; include quantity input, location selector, reason field (`web/ui/pages`, `internal/pages`).
- [X] IR-203 Add UI smoke test for stock receipt form rendering and POST flow to IR-201 (`internal/pages/ui_smoke_test.go`).

## Phase 3: Negative Inventory Policy
- [X] IR-301 Add pre-check to block negative inventory on checkout; return actionable error (`internal/pos`).
- [X] IR-302 Implement manager override backend with audit entry (actor, reason, snapshot as JSON: `{"item_id": "...", "location_id": "...", "qty_before": N}`) (`internal/pos`).
- [X] IR-303 Build manager override UI: reason textarea, auth check requiring `users.role IN ('manager', 'admin')`; wire to IR-302 handler (`web/ui/pages`, `internal/pages`).
- [X] IR-304 Add UI smoke test for override form rendering and auth enforcement (`internal/pages/ui_smoke_test.go`).

## Phase 4: Returns
- [X] IR-401 Implement return flow backend as new sale with `sale_type='return'` linked via `sale_links`; create positive movements (`internal/pos`, `internal/pages`).
- [X] IR-402 Ensure receipt template references original sale for returns (`web/ui/partials/receipt.html`).
- [X] IR-403 Build return flow UI: original sale lookup (receipt_no input), line selection, reason field; wire to IR-401 handler (`web/ui/pages`, `internal/pages`).
- [X] IR-404 Add UI smoke test for return form rendering and POST flow (`internal/pages/ui_smoke_test.go`).

## Phase 5: Low-Stock Alerts
- [X] IR-501 Implement low-stock flag calculation using `items.reorder_level` (already present in `001_init.sql`); expose handler endpoint in inventory API (`internal/pos`, `internal/pages`).
- [X] IR-502 Surface low-stock badges in catalog/inventory UI (visual indicator when `qty < reorder_level`) (`web/ui/pages`).
- [X] IR-503 Add UI smoke test verifying low-stock badge rendering (`internal/pages/ui_smoke_test.go`).

## Phase 6: Tests
- [X] IR-601 Unit tests for negative-inventory checks and override audit payload; include edge cases (missing reason, concurrent override attempts, zero/negative qty boundaries) (`internal/pos/inventory_test.go`).
- [X] IR-602 Integration test for return flow inventory effects and receipt linkage (`internal/pos/sales_test.go`).
- [X] IR-603 Unit tests for low-stock flag calculation with sample reorder_level data (`internal/pos/inventory_test.go`).
- [X] IR-604 UI smoke tests for override, return, and low-stock UI flows (`internal/pages/ui_smoke_test.go`).
