# Tasks: POS Core MVP

**Input**: `specs/002-pos-core-mvp/` (spec, plan, research, data-model, quickstart, contracts)  
**Prerequisites**: No schema changes; SQLite (`UT_STORE=sqlite`); responsive web UI

## Phase 0: Setup / Foundation

- [x] T001 Ensure `PRAGMA foreign_keys = ON` in DB bootstrap (`internal/db`); add guard if missing.
- [x] T002 Validate money/quantity helpers handle integer minor units and REAL for weighed items (`internal/pos` or shared helpers); add rounding rules for tax inclusive/exclusive.
- [x] T003 Add/confirm price resolution uses latest `price_history` row (open-ended) for item/variant (`internal/pos` pricing logic).
- [x] T004 Wrap sale persistence in DB transaction utilities to prevent orphan `sale_lines`/`payments` (`internal/pos`).

## Phase 1: User Story 1 – Complete a sale offline (P1)

- [x] T101 [P] Implement barcode/SKU lookup with fallback search (`internal/pos` + `internal/pages` handlers); prefer primary barcode tables.
- [x] T102 [P] Basket operations: add/edit/remove lines (incl. weighed) capturing snapshots (name, sku/barcode, tax_rate_bp, unit_price, totals) in `sale_lines`; exclude `is_active=0` catalog (`internal/pos`, `internal/pages`, `web/` templates).
- [x] T103 Tax calc respects `settings` inclusive/exclusive with integer-safe rounding; persist line tax and sale totals (`sales.subtotal`, `tax_total`, `total`, optional rounding) (`internal/pos`).
- [x] T104 Discounts: persist sale- and line-level discounts in `sale_discounts` (fixed/percent) and roll into `sales.discount_total` (`internal/pos`).
- [x] T105 Payments: support multiple tenders using seeded `payment_methods`; persist `payments` with amount/currency/reference/change_given/paid_at; block completion until covered (`internal/pos`, `internal/pages`).
- [x] T106 Completion flow: generate unique `receipt_no`, set `sales.status=completed`/`completed_at`, rollback on partial/failed tender (`internal/pos`).
- [x] T107 Stock movements: on completion create `stock_movements(type='sale')` per line with negative qty and `sale_line_id`; update `inventory` aggregates (`internal/pos`).
- [x] T108 Receipt payload: include lines, tax, discounts, payments, totals; printable/exportable via existing templates (`web/`); numeric receipt number with barcode + print/reset flow.
- [x] T109 Audit: record sale lifecycle (create, complete, void/park) with actor/payload in `audit_log` (`internal/pos` logging).
- [ ] T110 Implement explicit park/void flows with status transitions, UI handling, and audit entries; ensure parked sales resumable and voided sales blocked from completion (`internal/pos`, `internal/pages`, `web/`).

## Phase 2: User Story 2 – Manage catalog & pricing (P1)

- [ ] T201 [P] Expand catalog CRUD: create/update/deactivate items & variants; manage barcodes/images/category/brand/tax code; enforce `(item_id XOR variant_id)` on barcodes; honor `is_active` in UI/search (`internal/pos`, `internal/pages`, `web/`).
- [x] T202 [P] Price changes append new `price_history` row with timestamps; previous rows untouched (`internal/pos`).
- [x] T203 [P] Ensure inactive catalog records are hidden from sale selection and searches; add UI guard (`internal/pages`, `web/`).

## Phase 3: User Story 3 – Maintain inventory accuracy (P2)

- [ ] T301 [P] Stock receipts/adjustments: create `stock_movements` entries and update `inventory` aggregates per `stock_location` (`internal/pos`).
- [ ] T302 [P] Returns: model as new sales with `sale_type='return'`, link via `sale_links`, post positive movements; receipt reflects link (`internal/pos`, `internal/pages`, `web/`).
- [ ] T303 Negative inventory policy: block by default; allow explicit manager override with audit entry (`internal/pos`).
- [ ] T305 Surface low-stock alerts using reorder levels in UI/API; add tests for alert logic (`internal/pos`, `internal/pages`, `web/`).

## Phase 3.1: Safety & Audit additions

- [ ] T304 Implement manager override audit flow: require `manager`/`admin` approval, create `audit_log` entry `negative_inventory_override` with pre-sale inventory snapshot and reason (`internal/pos`, `internal/pages`).

## Phase 3.2: Shifts & Cash (P2)

- [ ] T306 Implement expected cash calculation from cash payments minus payouts/adjustments and persist on shift close (`internal/pos`).
- [ ] T307 Add payout/adjustment inputs stored for reconciliation and factored into expected cash; ensure tests cover calculations (`internal/pages`, `internal/pos`).

## Phase 4: User Story 4 – Basic plugin host operability (P3)

- [ ] T401 [P] Persist plugin manifest data into `plugins`, `plugin_entries`, `plugin_settings`, `plugin_permissions`, `plugin_hooks` per contracts (`internal/plugins`).
- [ ] T402 [P] Render plugin entries (page/button/popup/customer_facing/receipt_template) in UI shell and enforce declared permissions/capabilities (`internal/plugins`, `internal/pages`, `web/`).
- [ ] T403 Audit plugin enable/disable/install actions in `audit_log` with actor/payload (`internal/plugins`).

## Phase 4.1: Plugin Contracts & Tests

- [ ] T404 Define and persist minimal plugin permission contract in `specs/001-pos-core-mvp/contracts/permissions.md` and implement runtime enforcement checks in `internal/plugins` (`internal/plugins` + tests).
- [ ] T405 Add unit/handler tests for plugin permission enforcement, including denial and audit entries (`internal/plugins` tests).
- [ ] T406 Implement event dispatch semantics: tag blocking vs non-blocking plugin events; enforce rollback/audit for blocking, audit+continue for non-blocking (`internal/plugins`).
- [ ] T407 Add crash-isolation test for plugin event dispatch to ensure DB invariants; audit plugin handler errors (`internal/plugins` tests).
- [ ] T408 Marketplace install flow: list binaries per OS/arch, verify checksum, set default trust level `untrusted`, and expose trust/approve UI; tests for checksum mismatch/success (`internal/plugins`, `internal/pages`, `web/`).

## Phase 5: Tests & Validation

- [x] T501 Unit tests for pricing/tax rounding, price resolution, and discount application (`internal/pos` tests).
- [x] T502 Unit tests for stock movement generation and inventory aggregation (`internal/pos` tests).
- [ ] T503 Unit/handler tests for plugin permission enforcement (deny/allow) (`internal/plugins` tests).
- [ ] T504 Manual quickstart run with SQLite (`quickstart.md` flow) covering scan→cart→payment→receipt and a return; verify DB rows.

## Phase 5.1: Additional Test Tasks

- [ ] T505 Add deterministic rounding tests with examples (half-up) for weighted and unweighted items; ensure `sales.rounding` behavior verified (`internal/pos` tests).
- [ ] T506 Implement receipt-number concurrency test: simulate concurrent checkout goroutines/processes and assert unique, monotonic receipt numbers (`internal/pos` tests).
- [ ] T507 Payment partial-failure tests: simulate external payment capture failures and verify DB transaction and recoverable payment states; include retry path tests (`internal/pos` + `internal/plugins` tests).
- [ ] T508 Shift reconciliation tests: implement expected_cash calculation unit tests using seeded payments and payouts (`internal/pos` tests).
- [ ] T509 Add `shortcut_buttons` UI test to ensure quick-add mapping and correct quantity behavior (`web/` + `internal/pages`).
- [ ] T510 Performance benchmark: add a simple benchmark that runs sale completion end-to-end against an in-memory SQLite DB and asserts completion time under configured threshold (configurable CI warn only) (`internal/pos` tests / `scripts/bench`).

## Phase 6: Polish

- [ ] T601 Update any user-facing docs/help in `docs/` or `web/` if UI changes materially.
- [ ] T602 Review logging to avoid sensitive data (payments) and ensure clarity for audits.

## Issue‑Ready Tasks (expanded)

Below each task is expanded into an issue-ready checklist: description, acceptance criteria, tests, estimate, labels, and dependencies. Use the `T-` ID in the issue title for traceability.

- T201 — Catalog CRUD & UX (P1)
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

- T301 — Stock receipts & inventory aggregates (P2)
	- Summary: Implement stock receipt/adjustment endpoints that create `stock_movements` and update `inventory` aggregates per `stock_location`.
	- Acceptance Criteria:
		- Stock receipt creates `stock_movements` rows with correct sign and metadata.
		- Inventory aggregate calculation functions produce expected totals from movements.
	- Tests:
		- Unit tests for `inventory` aggregation given sample movements.
		- Integration test: post a stock receipt and assert inventory increased.
	- Estimate: 2d
	- Labels: `area:inventory`, `priority:P2`

- T302 — Returns as sale reversals (P2)
	- Summary: Model returns as sales with `sale_type='return'`; create linkage rows via `sale_links` and post positive stock movements.
	- Acceptance Criteria:
		- Return flow creates a sale with negative totals (or appropriately flagged) and corresponding stock movements that increment inventory.
		- Receipts for returns reference original sale via `sale_links`.
	- Tests:
		- Unit test for return sale creation and stock movements.
		- Integration smoke: complete a sale, perform return, validate inventory.
	- Estimate: 2d
	- Labels: `area:returns`, `priority:P2`

- T303 — Negative inventory policy & manager override (P2)
	- Summary: Default behavior blocks checkout that would create negative inventory, allow manager override with audit logging.
	- Acceptance Criteria:
		- Checkout attempt that would create negative inventory returns an actionable error.
		- Manager override flow exists in UI and creates `audit_log` entry with `user_id`, `reason`, `snapshot`.
	- Tests:
		- Unit tests for checkout pre-checks.
		- Integration test for override path and resulting audit row.
	- Estimate: 1.5d
	- Labels: `area:inventory`, `security`, `priority:P2`

- T401 — Plugin manifest persistence (P3)
	- Summary: Persist minimal manifest fields and expose a simple admin UI to view installed plugins.
	- Acceptance Criteria:
		- Manifest ingestion creates/updates `plugins` and `plugin_entries` rows.
		- Minimal admin page lists installed plugins and status.
	- Tests:
		- Unit tests for manifest parsing.
		- Handler test for admin page.
	- Estimate: 2d
	- Labels: `area:plugins`, `priority:P3`

 - T401b — Manifest verification & provenance (P3)
	- Summary: Verify plugin manifest checksums (SHA256) at install time, record provenance (source URL, uploader, checksum) and set default `trust_level = 'untrusted'` until manual trust is granted.
	- Acceptance Criteria:
		- Installer computes SHA256 and compares to manifest's declared checksum; mismatch aborts install.
		- `plugins` row stores provenance metadata (source, uploaded_by, checksum, installed_at).
		- Default `trust_level` is `untrusted` and UI shows a trust/approve action for `admin`.
	- Tests:
		- Unit test for checksum verification.
		- Integration test that simulates install with correct and incorrect checksums.
	- Estimate: 1d
	- Labels: `area:plugins`, `security`, `priority:P2`

 - T410 — Plugin process isolation & lifecycle (P3)
	- Summary: Implement host logic to launch plugins as separate OS processes, supervise lifecycle (start/stop/restart), and enforce timeouts/healthchecks.
	- Acceptance Criteria:
		- Host can start a plugin process from its manifest entrypoint and capture stdout/stderr.
		- Host health-checks plugin process and restarts on crash according to policy.
		- Lifecycle events are recorded in `audit_log` with `action` values like `plugin_started`, `plugin_crashed`, `plugin_restarted`.
	- Tests:
		- Integration test that starts a small test plugin binary and verifies lifecycle events and restart policy.
	- Estimate: 3d
	- Labels: `area:plugins`, `stability`, `priority:P2`

 - T411 — Plugin IPC / minimal gRPC contract (P3)
	- Summary: Define a minimal IPC/gRPC contract for event dispatch and RPC calls used by plugin types (payment, device, pricing). Provide a small reference server/client stub for integration tests.
	- Acceptance Criteria:
		- A one-page contract doc exists in `specs/pos-core-mvp/contracts/ipc.md` describing messages, timeouts, error model, and auth (if any).
		- Host implements a test stub to call `sale.completed` and receive an ack.
	- Tests:
		- Contract compliance test using the reference stub.
	- Estimate: 2d
	- Labels: `area:plugins`, `spec`, `priority:P2`

 - T412 — Plugin crash isolation & DB integrity test (P3)
	- Summary: Add an integration test that simulates plugin crash during an event dispatch (e.g., `sale.completed`) and asserts core DB invariants (no partial writes, no corrupted rows).
	- Acceptance Criteria:
		- Test injects a failing plugin that exits/crashes during event handling and verifies `sales` and `stock_movements` consistency after host recovery.
	- Tests:
		- Integration test with an intentional plugin crash.
	- Estimate: 2d
	- Labels: `area:plugins`, `stability`, `priority:P2`

- T402 — Plugin UI rendering & permission checks (P3)
	- Summary: Render plugin-provided entries (buttons/pages) and enforce runtime permission checks for host APIs.
	- Acceptance Criteria:
		- Plugin entries are rendered only when plugin declares capability and permission exists.
		- Host APIs return 403 for denied plugin actions and create an `audit_log` entry.
	- Tests:
		- Unit tests for permission checker.
		- Integration test rendering a permitted and denied plugin entry.
	- Estimate: 3d
	- Labels: `area:plugins`, `security`, `priority:P3`

- T505 — Deterministic rounding tests (P1)
	- Summary: Add unit tests that assert rounding behavior for tax-inclusive and exclusive scenarios, for weighted (REAL qty) and integer qty.
	- Acceptance Criteria:
		- Examples from spec included as test vectors and all pass.
	- Tests: table-driven unit test covering half-up scenarios.
	- Estimate: 1d
	- Labels: `area:pos`, `priority:P1`, `type:test`

- T506 — Receipt-number concurrency test (P1)
	- Summary: Implement a Go test that concurrently requests receipt numbers (goroutines and optional subprocess) and verifies uniqueness and monotonicity.
	- Acceptance Criteria:
		- Test asserts no duplicates and rising sequence under contention.
	- Tests: concurrency test included in `internal/pos` package.
	- Estimate: 1d
	- Labels: `area:pos`, `priority:P1`, `type:test`

- T507 — Payment partial-failure tests & compensation (P1)
	- Summary: Simulate payment provider failures and ensure DB transaction semantics and recoverable payment rows exist.
	- Acceptance Criteria:
		- On external capture failure, sale is not marked `completed`; `payments` rows record `status=failed` and are retryable.
	- Tests:
		- Unit test that simulates success/failure callbacks.
		- Integration test that runs payment flow with a fake payment plugin.
	- Estimate: 2d
	- Labels: `area:payments`, `priority:P1`

- T510 — Performance benchmark (P1)
	- Summary: Add a benchmark that runs sale completion end-to-end against a temp SQLite DB and records timings.
	- Acceptance Criteria:
		- Benchmark exists and can be run locally; CI warns if threshold exceeded.
	- Tests: Go benchmark under `internal/pos`.
	- Estimate: 1d
	- Labels: `area:perf`, `type:benchmark`

- How to use these issue-ready tasks
	- Create GitHub issues with title `T###: short description` copying the Summary and Acceptance Criteria into the issue body. Add labels from above and link PRs to the issue.
	- For estimates, `d` = days of focused work by an experienced contributor; adjust per team velocity.
	- If you want, I can open draft issues from these tasks and/or create a branch + PR scaffold for the highest-priority items (T201, T101/T102/T106).
