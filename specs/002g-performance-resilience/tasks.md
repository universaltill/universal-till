# Tasks: Performance & Resilience (002g)

## Phase 1: Benchmarks & Smoke
- [ ] PRS-101 Add sale-flow benchmark against temp SQLite with configurable threshold; wire optional CI warn.
- [ ] PRS-102 Add smoke script to run offline sale end-to-end; document usage.

## Phase 2: Event Dispatch Semantics
- [ ] PRS-201 Implement blocking vs non-blocking plugin event handling with rollback/audit rules; document defaults.
- [ ] PRS-202 Tests for blocking (rollback on failure) and non-blocking (audit + continue) paths.

## Phase 3: Crash Isolation & Resilience
- [ ] PRS-301 Integration test simulating plugin crash during event dispatch; assert DB invariants intact.
- [ ] PRS-302 Validate offline error handling paths for key flows (sale, inventory) and add regression tests.

## Phase 4: Docs
- [ ] PRS-401 Update contracts docs with event dispatch rules; add benchmark/smoke instructions to quickstart.
