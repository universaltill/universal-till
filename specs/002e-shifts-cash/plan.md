# Plan: Shifts & Cash Drawer (002e)

Branch: `002-pos-core-mvp` | Inputs: specs/002e-shifts-cash/spec.md

## Summary
Deliver shift lifecycle (open/close) with expected cash computation and audit logging using existing schema and local SQLite store.

## Phases
1) Data & Helpers
- Review shift/payment data access; add helpers to compute expected cash from cash payments minus payouts/adjustments.

2) Shift Lifecycle
- Implement open/close handlers; persist opening/closing/expected cash and notes.
- Validate register+cashier associations per existing models.

3) Audit
- Emit audit entries for shift open/close actions with actor/context.

4) Tests
- Unit tests for expected cash calculations with sample data.
- Handler tests for open/close flows and audit emission.

## Constraints
- No schema changes; integer money; offline-first.

## Deliverables
- Shift handlers, expected cash helper, audit logging, tests.
