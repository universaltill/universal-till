# Shifts & Cash Drawer (002e)

Status: Draft
Principles: offline-first; integer money; no schema changes

## Purpose & Goals
- Implement shift open/close flows with expected cash calculation.
- Record payouts/adjustments inputs and audit significant actions.

## Scope
- Shift lifecycle (open/close) per register+cashier.
- Expected cash computation from cash payments minus payouts/adjustments.
- Audit logging for shift actions.

## Non-Goals
- Sale payment capture, inventory movements (covered elsewhere).

## Functional Requirements
- Shift open records opening_cash and actor; close records closing_cash, expected_cash, notes.
- Expected cash derived from recorded cash payments during shift minus payouts/adjustments (manual inputs).
- Audit log records shift open/close events.

## Acceptance Criteria
- Open/close flows persist correctly and compute expected cash; tests cover calculations.
- Audit entries created for shift lifecycle events.
