# Tasks: Performance & Resilience (002g)

## Phase 1: Benchmarks & Smoke
- [X] PRS-101 Add sale-flow benchmark against temp SQLite with configurable threshold; wire CI warn/fail and document thresholds.
- [X] PRS-102 Add smoke script to run offline sale end-to-end; document usage.
- [X] PRS-103 Add micro-interaction benchmark/smoke (item lookup or cart add) with thresholds; wire CI warn/fail and document hardware assumptions.

## Phase 2: Event Dispatch Semantics
- [X] PRS-201 Implement blocking vs non-blocking plugin event handling with rollback/audit rules; document defaults.
- [X] PRS-202 Tests for blocking (rollback on failure) and non-blocking (audit + continue) paths.

## Phase 3: Crash Isolation & Resilience
- [X] PRS-301 Integration test simulating plugin crash during event dispatch; assert DB invariants intact.
  - **Acceptance**: Mock plugin crashes during sale.completed event. Assert: (1) sales.total = SUM(payments.amount), (2) inventory.quantity = SUM(stock_movements.quantity), (3) no orphaned sale_lines, (4) audit_log entry for crash exists. Remove skip from integration_test.go:L148.
- [X] PRS-302 Validate offline error handling paths for key flows (sale, inventory) and add regression tests.
  - **Acceptance**: Add tests for: (a) sale completion with network unavailable, (b) inventory lookup with missing/corrupted data, (c) payment provider timeout/unreachable. All flows must complete or fail gracefully without data corruption.

## Phase 4: Docs
- [X] PRS-401 Update contracts docs with event dispatch rules; add benchmark/smoke instructions to quickstart including hardware assumptions and warn/fail thresholds.
