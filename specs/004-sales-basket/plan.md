# Plan: Sales Flow & Basket (002b)

Branch: `002-pos-core-mvp` | Inputs: specs/002b-sales-basket/spec.md

## Summary
Deliver basket management, tax/discount handling, sale-line snapshots, and park/void flows with audit hooks, using existing schema and templates.

## Phases
1) Domain & Helpers
- Review current sale models; add snapshot structs and rounding utilities.
- Ensure inactive catalog filtered in selection helpers.

2) Basket Operations
- Implement add/edit/remove including weighed items; capture snapshots and discounts.
- Update handlers/templates for basket UI (HTMX where applicable).

3) Status Transitions
- Implement park and void flows with clear status enums and audit logging.
- Ensure receipt payload generation aligns with templates.

4) Testing
- Unit tests for rounding/tax/discount math and snapshot persistence.
- Handler tests for basket changes and park/void actions.

## Constraints
- No schema changes; integer money; offline-first.
- Domain logic testable without UI/network.

## Deliverables
- Basket APIs/handlers/templates.
- Park/void status handling + audit hooks.
- Tests covering math and lifecycle.
