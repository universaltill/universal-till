# Plan: Payments & Receipt Numbers (002c)

Branch: `002-pos-core-mvp` | Inputs: specs/002c-payments-receipts/spec.md

## Summary
Implement multi-tender payments with safe completion gating, partial-failure handling, and a receipt number generator that is unique and concurrency-safe using the existing `sales.receipt_no` column (no new tables/migrations).

## Phases
1) Transaction Boundaries
- Define payment/save/complete transaction flow; ensure rollback on failure.
- Add statuses for pending/failed/succeeded as needed in code (no schema change).

2) Payment Handling
- Support multiple tenders, change_given, references; block completion until coverage.
- Simulate provider/plugin failures and ensure recoverable state.

3) Receipt Numbering
- Use existing `sales.receipt_no` (UNIQUE) as the sequence; implement transactional allocation (e.g., max(receipt_no)+1 or rowid-based) with locking.
- Add concurrency test to guarantee uniqueness/monotonicity with the no-migration approach.

4) Tests
- Unit/integration tests for success/failure paths, retry flows, and concurrency receipt test.
- Bench small timing if needed.

## Constraints
- No schema changes for receipt numbering; use existing column.
- Offline-first; avoid external dependencies.

## Deliverables
- Payment completion flow with failure handling.
- Receipt generator + concurrency test.
- Documentation of sequence approach and any migration decision.
