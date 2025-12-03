# Tasks: Payments & Receipt Numbers (002c)

## Phase 1: Flow Definition
- [x] PR-101 Define payment transaction boundaries; ensure rollback on failure before marking sale completed (`internal/pos`).

## Phase 2: Payment Handling
- [ ] PR-201 Implement multi-tender payments with change/reference capture; block completion until fully paid (`internal/pos`, `internal/pages`).
- [ ] PR-202 Simulate payment provider/plugin failures; persist recoverable failed status; add retry path tests.

## Phase 3: Receipt Numbering
- [ ] PR-301 Implement generator using existing `sales.receipt_no` (UNIQUE) with transactional allocation and locking; no new tables/migrations.
- [ ] PR-302 Ensure persistence across restarts; add concurrency test (goroutines/subprocess) to assert uniqueness/monotonicity.

## Phase 4: Tests & Docs
- [ ] PR-401 Unit/integration tests for payment success/failure and retry.
- [ ] PR-402 Update docs/quickstart if payment flow UX changes; note sequence approach.
