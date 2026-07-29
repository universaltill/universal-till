# Test coverage batch 1: catalog_repo, shortcuts_repo, inventory_api

2026-07-29

Farshid asked for near-100% test coverage across the codebase (unit, smoke,
UI/UX, e2e) covering everything done this session and going forward, and
that TDD become standard practice for new work. This is batch 1 of a
multi-batch backfill effort, prioritized by business risk: catalog CRUD and
inventory API were picked first because they're directly tied to bugs found
earlier tonight (missing inventory rows, stale image cache, filter reset).

## What changed

- `internal/data/catalog_repo_crud_test.go` (new) — the ~23 previously
  0%-covered `CatalogRepo` methods: barcode/SKU existence checks, category
  ensure/idempotency, item/variant label lookups, list/export, lookup
  validation, deactivate cascade, cost/price/name mutation (including the
  item→variant fallback used by the cloud's remote directives, ADR-0018),
  and barcode attach/reassignment rules.
- `internal/data/shortcuts_repo_crud_test.go` (new) — `SaveButtons` (whole-set
  replace), `UpdateOrder` (drag&drop persistence), `AddButton` (validation,
  active-item requirement, upsert-in-place), `RemoveButton`.
- `internal/pages/inventory_api_test.go` (new) — HTTP-level tests for
  `CreateStockReceipt`, `CreateNegativeInventoryOverride` (including the
  full manager-PIN approval flow: no PIN → 403, wrong PIN → 403, correct PIN
  → 200 with the **approving manager**, not the requesting cashier, recorded
  as the audit actor), `CreateReturn` (by sale id and by receipt number,
  quantity/line validation), `GetLowStock`.
- `internal/testsupport/sqlite_catalog.go` — added `items.cost_price` and an
  `inventory` table to the shared minimal catalog test schema (both present
  in the real schema, `internal/db/migrations/001_init.sql`, just missing
  from this hand-rolled fixture). Deliberately did NOT add `stock_locations`
  — `TestCreateItem_SucceedsWithoutStockLocationsTable` depends on it being
  absent to exercise `ensureInventoryRow`'s best-effort failure path.
- `internal/pages/ui_smoke_test.go` — added `is_active` to the `users` table
  in `seedForPages`, matching the real schema. Without it,
  `AuthRepo.ListActiveUsersWithPIN` (which the manager-PIN override flow
  depends on) failed with "no such column: is_active" — a real gap in the
  shared test fixture that nothing had previously exercised.

## Independent review (opus, per standing process)

Ran build/tests/guards, checked schema fidelity against the real migration,
checked for order-dependent or padding-only tests. Verdict: no blocking
issues, schema changes verified to match `001_init.sql` exactly, no test
reads as coverage-padding. One real gap flagged and fixed before commit:

**Fixed**: the correct-manager-PIN test asserted only HTTP 200, not that the
approving manager actually became the audit actor — the security-relevant
part of the whole PIN-approval flow (a wrong actor recorded here would mean
overrides are mis-attributed in the audit trail). Now queries
`audit_log.actor_id` for the returned override id and asserts it's the
manager's id, not the cashier's.

**Noted, not changed** (nitpicks, no regression-guard loss): the JSON-accept
low-stock assertion only checks the `count` field is present, not its value;
the receipt/return happy-path tests assert HTTP-level success only (the
actual DB mutation is already covered by `internal/pos` tests); the
`testsupport` in-memory `inventory` table omits the real schema's UNIQUE/
CHECK constraints (harmless — nothing in this batch writes to it).

## Verification

`go build ./...` and `go test ./...` pass. Also spot-checked with
`-shuffle=on` on `internal/data` and `internal/pages` — no order dependence.
`scripts/ci/guard-data-access.sh` and `scripts/ci/guard-i18n.sh` both pass.

## Coverage delta (this batch's files)

- `internal/data/catalog_repo.go`: ~0% → 66–100% per function.
- `internal/data/shortcuts_repo.go`: ~0% → 56–100% per function.
- `internal/pages/inventory_api.go`: ~0% → 66–100% per function.

## Remaining backlog

Tracked as in-session tasks, largest first: `internal/data/pos_repo.go`
(~150 untested functions — the single biggest gap, highest business risk,
sales/shift/refund/inventory logic), `internal/data/plugin_repo.go` (~40),
`internal/data/auth_repo.go` (security-critical, currently 0%), several
smaller `internal/data` repos, `internal/pages/*` handler packages, and
low-coverage infrastructure packages (`internal/server`, `internal/selfupdate`,
`internal/updates`, `internal/plugins/storage`, `internal/ui`) — some of
which may turn out to be OS/process-boundary code not practically
unit-testable, to be assessed and documented rather than padded.
