# Plan: Inventory Movements & Returns (002d)

Branch: `002-pos-core-mvp` | Inputs: specs/002d-inventory-returns/spec.md

## Summary
Deliver inventory movement handling (receipts/adjustments), negative-inventory policy with override/audit, return flows with linked sales, and low-stock alert surfacing, all using existing schema.

## Phases
1) Inventory Foundations
- Review movement tables and aggregation helpers; add tests for aggregate correctness.

2) Receipts & Adjustments
- Implement APIs/handlers to create stock movements and update aggregates per location.

3) Negative Inventory Policy
- Add pre-checks to block negative inventory on checkout; implement manager override flow with audit payload.

4) Returns
- Implement return-as-sale with `sale_type='return'`, `sale_links`, and positive stock movements; ensure receipts reference original sale.

5) Low-Stock Alerts
- Calculate low-stock flags using `items.reorder_level` (column added to `001_init.sql`); surface in catalog/inventory UI as badges and expose handler endpoint via API (no automated ordering/notifications beyond visual flags).

6) Tests
- Unit tests for aggregation, negative-inventory checks, overrides, returns, and low-stock flags.
- Integration tests for receipt/adjustment and return flows.

## Constraints
- Schema amendment: added `items.reorder_level` column to `001_init.sql` (pre-release); integer money; offline-first.

## Deliverables
- Inventory APIs/handlers, override/audit logic, return handling, low-stock surfacing, and tests.
