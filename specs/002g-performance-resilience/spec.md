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
- Event dispatch defaults to non-blocking with audit for non-critical events; blocking paths explicitly marked with rollback semantics.
- Crash isolation: plugin crash during events must not corrupt DB; tests verify invariants.
- Offline safety: flows must operate without network; errors handled gracefully.

## Acceptance Criteria
- Benchmark/CI script exists and runs sale flow against temp SQLite; threshold configurable.
- Event dispatch code distinguishes blocking vs non-blocking and documents rollback; tests cover both.
- Crash isolation test simulates plugin crash during event dispatch and asserts DB invariants.
- Smoke script documents offline run path; results reproducible locally.
