# Implementation Plan: POS Core MVP

**Branch**: `[001-pos-core-mvp]` | **Date**: 2025-11-27 | **Spec**: specs/pos-core-mvp/spec.md  
**Input**: Feature specification from `specs/pos-core-mvp/spec.md`

## Summary

Deliver the POS core MVP—catalog management, sales (offline), inventory integrity, and basic plugins host—using the existing SQLite schema (`internal/db/migrations/001_init.sql`) and responsive web UI. No schema changes; all money stays integer minor units. Ensure barcode/price resolution, split payments, tax-inclusive/exclusive handling, stock movements, returns, and plugin metadata/UI rendering work offline and atomically.

## Technical Context

**Language/Version**: Go 1.25 (per `go.mod`), SQLite via `modernc.org/sqlite`; responsive web UI (htmx + Alpine.js) served by Go.  
**Primary Dependencies**: Go standard library, `database/sql` + `modernc.org/sqlite`, existing template/static assets, UUID, LRU cache.  
**Storage**: SQLite file (default `data/unitill.db`) when `UT_STORE=sqlite`; JSON legacy fallback for some data.  
**Testing**: `go test ./...`; add focused unit tests in `internal/pos`, `internal/plugins`, `internal/ui`; manual smoke with SQLite DB.  
**Target Platform**: Local-first POS on Linux/macOS + ARM (Raspberry Pi-class); browser-based responsive UI.  
**Project Type**: Single Go binary serving HTML UI and plugin host.  
**Performance Goals**: Sale completion <5s offline on modest hardware; UI interactions sub-200ms locally.  
**Constraints**: Offline-first; integer money; no schema changes; plugin contracts stable and enforced.  
- Changes MUST respect the existing SQLite schema defined in `internal/db/migrations/001_init.sql` and documented in `docs/data-model.md`.  
- Do not rename or drop columns/tables; no new migrations for this MVP.  
**Scale/Scope**: Single-store, single/limited registers; small-to-mid catalog (few k SKUs) on embedded hardware.

## Constitution Check

- Correctness over cleverness: explicit calculations, integer money, clear rounding.  
- Stable domain model: no schema edits; append-only history.  
- Predictable plugin contracts: use existing plugin tables; enforce declared permissions.  
- Local-first & resilient: full sale flow offline; no external dependencies.  
- Testable core: domain logic testable without network/UI; side effects isolated.  
- Security & data integrity: validate inputs, FK on, avoid logging sensitive data.  
Status: Gates pass with above commitments.

## Project Structure

### Documentation (this feature)

```text
specs/pos-core-mvp/
├── plan.md              # This file
├── research.md          # Phase 0 notes
├── data-model.md        # Feature-specific data model mapping (no schema changes)
├── quickstart.md        # How to run/test this feature slice
├── contracts/           # Plugin host contract notes for this feature
└── tasks.md             # To be generated via /speckit.tasks
```

### Source Code (repository root)

```text
main.go
internal/
  config/        # env/config parsing
  db/            # migrations, SQLite wiring
  pos/           # domain logic for catalog, sales, inventory
  plugins/       # plugin runtime and metadata handling
  pages/         # HTTP handlers/views
  ui/            # UI helpers/layout
  httpx/         # HTMX helpers
  server/        # server wiring
  settings/      # settings management (tax, currency)
web/             # templates, assets, themes (responsive)
data/            # sample buttons/DB files
proto/           # (if used)
```

**Structure Decision**: Single Go binary serving responsive web UI and plugin host; reuse existing directories above.

## Milestones & Plan

### Phase 0 – Research (output: research.md)
- Review `internal/db/migrations/001_init.sql` and `docs/data-model.md`; map money/quantity handling, FK constraints, price/history rules.
- Inventory current flows in `internal/pos`, `internal/pages`, `internal/ui`, `internal/plugins` to identify entrypoints for catalog, sales, inventory, plugins host.
- Capture ambiguities (rounding rules, negative stock policy, plugin permission enforcement) in `research.md`.

### Phase 1 – Design (outputs: data-model.md, quickstart.md, contracts/)
- Document price resolution (latest `price_history` row), tax-inclusive/exclusive calculations, and stock movement rules; note zero schema changes.
- Define UI flows for scan/basket/payment/receipt and catalog management using existing templates; ensure responsive behavior.
- Summarize plugin manifest ingestion and rendering paths; document minimal host contract in `contracts/`.
- Write quickstart steps for running with SQLite (`UT_STORE=sqlite`) and seed data for manual testing.

### Phase 1 – Implementation (detailed)

Timebox: 4 weeks (can be split into two 2-week sprints). Goal: deliver a minimally usable, well-tested POS core supporting catalog, sales (offline), inventory updates, returns, payments (multi‑tender), and a minimal plugin host surface.

Sprint cadence and scope:
- Week 1 — Catalog & Pricing Core
  - Implement and harden catalog CRUD paths used by the UI and API handlers. Ensure barcode lookup, image paths, and active/inactive flags behave as expected.
  - Implement `price_history` read/write helpers and price resolution logic used by the sale flow. Add unit tests for price selection and edge cases (no price, multiple prices same timestamp).
  - Deliverable: catalog CRUD + pricing helpers with unit tests and docs updated in `data-model.md`.
  - Acceptance criteria: all new/changed functions covered by unit tests; no DB schema changes; `go test ./internal/pos -run TestPrice|TestCatalog` passes locally.

- Week 2 — Sale Flow & Basket
  - Implement basket operations (add/edit/remove, weighed items) and snapshot semantics for `sale_lines` so price/tax at time-of-sale is preserved.
  - Wire basket -> payment initiation UI flow and server endpoints. Implement client-side hooks for UX (HTMX snippets) to avoid full page reloads.
  - Deliverable: end-to-end scan→cart→review flow (UI + handlers) with unit tests for snapshot semantics.
  - Acceptance criteria: sale lines preserve price/tax snapshot in tests; manual smoke validates scan→cart→checkout flows using seed DB.

- Week 3 — Payments, Receipt Generator & Inventory
  - Implement multi-tender payments recording and the atomic DB transaction pattern for sale finalisation described in this plan: core DB changes inside a transaction; external plugin/payment capture outside with compensating state recorded on failure.
  - Implement DB-backed receipt number generator and add concurrency test that spawns goroutines to request numbers concurrently; assert uniqueness and monotonicity.
  - Implement stock movement writes on sale completion and return flows; add aggregation helpers and unit tests validating inventory after sales/returns.
  - Deliverable: payments write-path, receipt generator, stock_movement integration, and tests.
  - Acceptance criteria: payment workflow tests (success + simulated partial failure) pass; receipt concurrency test passes locally.

- Week 4 — Plugin Host Minimal Surface, Audit & Release Prep
  - Implement plugin manifest ingestion (persist minimal manifest fields), permission checks at runtime for the host, and a rendering path for plugin-provided UI entries (buttons/pages) in a safe, permissioned way.
  - Add audit logging for manager overrides and negative inventory overrides; ensure override actions create auditable rows with `user_id`, `reason`, and `timestamp`.
  - Finish remaining unit tests and add at least one integration smoke script that runs a full sale in a temp SQLite DB and verifies DB invariants.
  - Deliverable: minimal plugin host support for manifests + permission enforcement, audit logging, integration smoke script.
  - Acceptance criteria: plugin permission enforcement unit tests pass; audit entries appear for override paths; smoke script exits 0.

Milestone sign-off criteria (for each sprint):
- Unit tests added/updated for changed packages; `go test ./...` passes.
- CI gates: `gofmt`/`go vet` pass, linter (if present) passes; no new DB migration files added or modified.
- Performance quick-check: the provided smoke benchmark for sale completion must complete within configured threshold on CI runner (threshold configurable; default 5s for constrained hardware check — CI may run a looser threshold).
- Documentation: `data-model.md` and `quickstart.md` updated with any behavioral changes and manual verification steps.

CI & gating recommendations
- Required CI jobs for this feature branch:
  - `unit-tests`: `go test ./...` (fail on any package test failure)
  - `fmt-vet`: `gofmt -l` + `go vet`
  - `receipt-concurrency-test`: run a small Go test that stresses the receipt generator under concurrency
  - `smoke`: run the integration smoke script against a temporary SQLite file (created and destroyed during job)
  - `bench-check` (optional): run a short benchmark and fail the job if sale completion median exceeds the threshold for the runner class; treat as advisory for now.

Delivery checklist (before merge to `main`):
- All unit and integration tests passing locally.
- Smoke script demonstrates an offline sale (scan → cart → pay → receipt) against a seeded SQLite DB.
- Receipt concurrency test added and passing.
- No modifications to `internal/db/migrations/*.sql` (verify with CI file check).
- Update agent context (`.specify/scripts/bash/update-agent-context.sh copilot`) to refresh Copilot guidance.

Owners & assignments
- Suggested: break tasks into issues and assign to implementers. Use `T-` task IDs from `tasks.md` for traceability. If no assignee, use `unassigned` label and link PR to spec.

Rollout & verification
- Merge to `main` after sign-off and CI green. Run the manual smoke quickstart steps from `quickstart.md` on a test machine (Linux or macOS) to validate UI flows and receipts.
- Post-merge: monitor logs for unexpected DB errors, receipt generator anomalies, and plugin permission denials; collect feedback and cut follow-up patches.

### Phase 2 – Implementation Breakdown (feeds `/speckit.tasks`)
- Catalog/Pricing: CRUD respects `is_active`, barcodes/images; append-only `price_history`; fallback search when barcode missing.
- Sales Flow: basket add/edit/remove (incl. weighed), snapshot fields on `sale_lines`, tax calc per settings (inclusive/exclusive), discounts persisted.
- Payments: multi-tender using seeded `payment_methods`; completion gating on full payment; safe rollback on partial failures.
- Inventory: post sale/return `stock_movements`; aggregate `inventory`; enforce negative stock policy with override logging.
- Returns: model as new sales with `sale_type='return'` and `sale_links`; receipts reflect links; stock increments correctly.
- Plugin Host: persist manifest data into plugin tables; render entries (page/button/popup/customer_facing/receipt_template); enforce permissions/capabilities.
- Audit & Resilience: log lifecycle events; ensure transactions prevent orphan rows; offline-safe error handling.

## Testing & Validation

- Unit tests: extend `internal/pos` (pricing/tax, inventory aggregation, sale completion/returns) and plugin host permission enforcement.  
- Integration/smoke: `go test ./...`; manual run `UT_STORE=sqlite ./bin/unitill-pos` (or `make build && ./bin/unitill-pos`) to exercise scan→cart→payment→receipt offline.  
- Data integrity checks: verify FK enforcement and `(item_id XOR variant_id)` constraints in tests; ensure `price_history` append-only; confirm stock movements align with inventory aggregates.

### Payment Transaction Model (explicit)

- Payment and sale completion transaction boundaries:
  - All core sale finalisation DB changes (creating/updating `sales`, `sale_lines`, `sale_discounts`, `payments`, `stock_movements`, and `inventory` updates) must be executed in a single database transaction when possible. This ensures atomic visibility of a completed sale.
  - External operations (e.g., calling a payment gateway or plugin `payment.capture`) should be performed outside the DB transaction where practical. On success, perform a follow-up DB update to record `payments.reference`/`paid_at`. If an external call fails, the DB transaction must not mark the sale as `completed` and must either rollback or persist a recoverable `payments` row with status `failed` depending on local offline constraints.
  - Tests must simulate partial failure: e.g., payment capture success/fail after DB write, ensuring there is a clear, auditable intermediate state and a mechanism to retry/resolve.

### Receipt Number Generator

- Implement a DB-backed atomic generator (see `spec.md` guidance) and add a test that concurrently requests receipt numbers from multiple goroutines/processes to ensure uniqueness and monotonicity under contention.

### Performance Validation

- Add simple benchmarks and smoke checks to validate the performance goals (sale completion <5s in a constrained environment):
  - A benchmark that runs the sale completion flow against an in-memory or temp SQLite DB measuring end-to-end time.
  - A CI smoke script (optional) to run the benchmark and warn if thresholds exceed configured limits.

## Out of Scope / Non-goals

- No schema/migration changes beyond `001_init.sql`.  
- No new plugin contract types or cloud sync.  
- No hardware-specific device integrations beyond current stubs; no advanced reporting beyond receipts.

## Complexity Tracking

No constitution violations expected; no additional complexity justifications required.
