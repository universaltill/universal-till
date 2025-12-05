# Performance & Resilience (002g)

Status: Draft
Principles: offline-first; correctness over cleverness; testable core; no schema changes

## Purpose & Goals
- Validate performance targets (sale completion <5s on target hardware; sub-200ms local interactions).
- Define and enforce event dispatch semantics (blocking vs non-blocking) for plugins without compromising DB integrity.
- Add resilience tests: crash isolation, offline safety, benchmarks, smoke scripts.

## Scope
- Benchmarks/smoke scripts for sale flow performance on SQLite.
- Event dispatch rules and implementation notes (blocking vs non-blocking, rollback/audit).
- Crash isolation tests for plugin events; offline safety/error handling.

## Non-Goals
- Core feature functionality already covered in other slices.

## Functional Requirements
- Sale completion benchmark/smoke under target thresholds; CI warn/fail thresholds documented.
- Local interactions (e.g., item lookup or cart add) complete under 200ms on target hardware; micro-interaction benchmark validates latency (PRS-103).
- Event dispatch MUST default to non-blocking mode with audit for non-critical events. Blocking mode requires explicit configuration and implements rollback semantics on handler failure. Implementation (PRS-201) and tests (PRS-202) cover both modes.
- Crash isolation: plugin crash during events must not corrupt DB. Tests verify invariants: (1) sales.total = SUM(payments.amount), (2) inventory.quantity = SUM(stock_movements.quantity), (3) no orphaned sale_lines, (4) audit_log entry exists for crash event (PRS-301).
- Offline safety: flows must operate without network; errors handled gracefully with regression tests for offline scenarios (PRS-302).

## Acceptance Criteria
- Benchmark/CI script exists and runs sale flow against temp SQLite; threshold configurable.
- Target hardware defined as Raspberry Pi 4 (8GB) baseline: 4-core ARM64 @ 1.5GHz, 8GB RAM, SSD storage. "Equivalent mini PC" means similar CPU/RAM/storage performance; adjust thresholds for x86 or HDD with explicit justification. Thresholds (warn/fail) documented for sale and micro-interactions.
- Event dispatch code distinguishes blocking vs non-blocking and documents rollback; tests cover both.
- Crash isolation test simulates plugin crash during event dispatch and asserts DB invariants (enumerated in FR above).
- Smoke script documents offline run path; results reproducible locally.
