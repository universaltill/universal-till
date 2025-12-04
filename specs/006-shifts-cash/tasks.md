# Tasks: Shifts & Cash Drawer (002e)

## Phase 1: Foundations
- [X] SC-101 Review shift/payment data access; add helper to compute expected cash from cash payments minus payouts/adjustments; unit tests with sample data.

## Phase 2: Shift Lifecycle
- [X] SC-201 Implement shift open handler: persist opening cash, register+cashier, audit entry.
- [X] SC-202 Implement shift close handler: persist closing cash, expected cash, notes; validate associations; audit entry.

## Phase 3: Cash Adjustments
- [X] SC-301 Add inputs/handlers for payouts/adjustments affecting expected cash calculation; ensure stored for reconciliation.

## Phase 4: Audit & Validation
- [X] SC-401 Ensure audit logging for shift open/close (actor/context) and cash adjustments.
- [X] SC-402 Add handler tests for open/close/adjust flows; verify expected cash math in tests.

## Phase 5: Docs
- [X] SC-501 Update quickstart/feature notes if shift workflows change; document expected-cash formula and usage.
