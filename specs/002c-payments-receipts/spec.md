# Payments & Receipt Numbers (002c)

Status: Draft
Principles: offline-first, integer money; prefer no schema changes (see sequence note)

## Purpose & Goals
- Support multi-tender payments with completion gating and rollback on failure.
- Implement receipt number generation with concurrency safety.
- Cover payment partial-failure and recovery scenarios.

## Scope
- Payment capture/persistence, completion transaction boundaries.
- Receipt number generation strategy (decide approach without schema change or explicitly plan migration).
- Tests for concurrency and partial failures.

## Non-Goals
- Stock movements, returns, plugin hooks (covered elsewhere).

## Functional Requirements
- Payments persisted with amounts, references, status, and completion blocked until fully paid.
- Receipt numbers are unique/monotonic; survive restarts; concurrency safe.
- Failure handling: partial/failed payments do not mark sale completed; recoverable state recorded.

## Acceptance Criteria
- Payment flows support multiple tenders and roll back on failed capture; tests simulate failure.
- Receipt number generator uses existing `sales.receipt_no` (UNIQUE) with no new tables/migrations; concurrency test shows no duplicates and monotonic allocation.
- Decision recorded in plan/spec that no migration is introduced for receipt numbering.
