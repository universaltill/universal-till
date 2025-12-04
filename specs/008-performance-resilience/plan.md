# Plan: Performance & Resilience (002g)

Branch: `008-performance-resilience` | Inputs: specs/008-performance-resilience/spec.md

## Summary
Add performance benchmarks/smoke scripts, enforce event dispatch semantics (blocking vs non-blocking with rollback/audit), and resilience tests for crash isolation and offline safety using existing codepaths and SQLite.

## Phases
1) Benchmarks & Smoke
- Implement sale-flow benchmark against temp SQLite; configurable threshold; CI warn/fail mode.
- Add smoke script to run offline sale flow end-to-end.

2) Event Dispatch Semantics
- Document and implement blocking vs non-blocking plugin events; add rollback/audit handling where blocking.

3) Crash Isolation & Resilience
- Add tests simulating plugin crash during event dispatch; assert DB invariants and audit entries.
- Validate offline behavior/error handling in key flows.

4) Tests & Docs
- Benchmarks and integration tests wired into CI target list.
- Update contracts/quickstart with event dispatch rules and benchmark usage.

## Constraints
- No schema changes; offline-first; stable domain model; avoid external dependencies.

## Deliverables
- Benchmark + smoke script.
- Event dispatch semantics implemented/documented.
- Crash isolation and resilience tests.
