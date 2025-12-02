# POS Core MVP — Issue-ready Tasks

This file contains issue-ready expansions of the tasks in `tasks.md`. Each entry includes: Summary, Acceptance Criteria, Tests, Estimate, Labels, Dependencies.

## T201 — Catalog CRUD & UX (P1)
- Summary: Implement backend CRUD for `items`, `variants`, barcodes and expose handlers/pages for create/update/deactivate, image uploads, categories, brands and tax code assignment.
- Acceptance Criteria:
  - Create/update/deactivate endpoints exist and return appropriate HTTP codes.
  - UI forms allow creating/updating items and uploading image paths.
  - `(item_id XOR variant_id)` uniqueness enforced for barcodes at the application level.
  - `is_active=false` items/variants do not appear in sale searches or quick-add lists.
- Tests:
  - Unit tests for CRUD functions.
  - Integration test that creates item+variant+barcode and verifies sale search hides inactive records.
- Estimate: 2d backend + 2d UI
- Labels: `area:catalog`, `priority:P1`, `type:feature`
- Depends on: T202 (price_history behavior)

## T301 — Stock receipts & inventory aggregates (P2)
- Summary: Implement stock receipt/adjustment endpoints that create `stock_movements` and update `inventory` aggregates per `stock_location`.
- Acceptance Criteria:
  - Stock receipt creates `stock_movements` rows with correct sign and metadata.
  - Inventory aggregate calculation functions produce expected totals from movements.
- Tests:
  - Unit tests for `inventory` aggregation given sample movements.
  - Integration test: post a stock receipt and assert inventory increased.
- Estimate: 2d
- Labels: `area:inventory`, `priority:P2`

## T302 — Returns as sale reversals (P2)
- Summary: Model returns as sales with `sale_type='return'`; create linkage rows via `sale_links` and post positive stock movements.
- Acceptance Criteria:
  - Return flow creates a sale with negative totals (or appropriately flagged) and corresponding stock movements that increment inventory.
  - Receipts for returns reference original sale via `sale_links`.
- Tests:
  - Unit test for return sale creation and stock movements.
  - Integration smoke: complete a sale, perform return, validate inventory.
- Estimate: 2d
- Labels: `area:returns`, `priority:P2`

## T303 — Negative inventory policy & manager override (P2)
- Summary: Default behavior blocks checkout that would create negative inventory, allow manager override with audit logging.
- Acceptance Criteria:
  - Checkout attempt that would create negative inventory returns an actionable error.
  - Manager override flow exists in UI and creates `audit_log` entry with `user_id`, `reason`, `snapshot`.
- Tests:
  - Unit tests for checkout pre-checks.
  - Integration test for override path and resulting audit row.
- Estimate: 1.5d
- Labels: `area:inventory`, `security`, `priority:P2`

## T401 — Plugin manifest persistence (P3)
- Summary: Persist minimal manifest fields and expose a simple admin UI to view installed plugins.
- Acceptance Criteria:
  - Manifest ingestion creates/updates `plugins` and `plugin_entries` rows.
  - Minimal admin page lists installed plugins and status.
- Tests:
  - Unit tests for manifest parsing.
  - Handler test for admin page.
- Estimate: 2d
- Labels: `area:plugins`, `priority:P3`

## T402 — Plugin UI rendering & permission checks (P3)
- Summary: Render plugin-provided entries (buttons/pages) and enforce runtime permission checks for host APIs.
- Acceptance Criteria:
  - Plugin entries are rendered only when plugin declares capability and permission exists.
  - Host APIs return 403 for denied plugin actions and create an `audit_log` entry.
- Tests:
  - Unit tests for permission checker.
  - Integration test rendering a permitted and denied plugin entry.
- Estimate: 3d
- Labels: `area:plugins`, `security`, `priority:P3`

## T505 — Deterministic rounding tests (P1)
- Summary: Add unit tests that assert rounding behavior for tax-inclusive and exclusive scenarios, for weighted (REAL qty) and integer qty.
- Acceptance Criteria:
  - Examples from spec included as test vectors and all pass.
- Tests: table-driven unit test covering half-up scenarios.
- Estimate: 1d
- Labels: `area:pos`, `priority:P1`, `type:test`

## T506 — Receipt-number concurrency test (P1)
- Summary: Implement a Go test that concurrently requests receipt numbers (goroutines and optional subprocess) and verifies uniqueness and monotonicity.
- Acceptance Criteria:
  - Test asserts no duplicates and rising sequence under contention.
- Tests: concurrency test included in `internal/pos` package.
- Estimate: 1d
- Labels: `area:pos`, `priority:P1`, `type:test`

## T507 — Payment partial-failure tests & compensation (P1)
- Summary: Simulate payment provider failures and ensure DB transaction semantics and recoverable payment rows exist.
- Acceptance Criteria:
  - On external capture failure, sale is not marked `completed`; `payments` rows record `status=failed` and are retryable.
- Tests:
  - Unit test that simulates success/failure callbacks.
  - Integration test that runs payment flow with a fake payment plugin.
- Estimate: 2d
- Labels: `area:payments`, `priority:P1`

## T510 — Performance benchmark (P1)
- Summary: Add a benchmark that runs sale completion end-to-end against a temp SQLite DB and records timings.
- Acceptance Criteria:
  - Benchmark exists and can be run locally; CI warns if threshold exceeded.
- Tests: Go benchmark under `internal/pos`.
- Estimate: 1d
- Labels: `area:perf`, `type:benchmark`

---

If you prefer the original `tasks.md` to be replaced, I can update it in-place. I can also open GitHub draft issues for selected T-IDs and create branches/PR scaffolds — tell me which T-IDs to prioritize.
