# Tasks: SQL Access Refactor to Data Repos

## Phase 1: Setup
- [X] T001 Confirm repo entrypoints and db handles for repositories (internal/data/*_repo.go) and note existing repos: internal/data/catalog_repo.go, internal/data/pos_repo.go, internal/data/plugin_repo.go.
- [X] T002 Add new repo stubs if needed: internal/data/settings_repo.go (settings CRUD), internal/data/shortcuts_repo.go (shortcut_buttons), and any plugin-specific helper files under internal/data/plugin_repo.go (or subfiles) before wiring callers.

## Phase 2: Foundational
- [X] T003 Document transaction/ctx patterns to follow in repo methods (accept *sql.DB and *sql.Tx) in specs/001-sql-repo-refactor/data-model.md for reference while moving callers.
- [X] T004 [P] Add shared repo error/telemetry helpers (if missing) for consistent logging/metrics and error wrapping used by all repos.

## Phase 3: User Story 1 – Centralize Data Access (P1)
- [X] T005 [P] [US1] Move settings SQL into a new internal/data/settings_repo.go; update internal/settings/settings.go to call the repo only.
- [X] T006 [P] [US1] Move payment-method lookup in internal/pages/index_page.go into pos repo; expose a repo method and update caller.
- [X] T007 [P] [US1] Move buttons/shortcut SQL out of internal/ui/buttons.go into internal/data/shortcuts_repo.go (or pos repo); update callers.
- [X] T008 [P] [US1] Move catalog CRUD/search SQL from internal/pos/catalog.go and catalog_search.go into pos repo methods; update pos consumers.
- [X] T009 [P] [US1] Move inventory reads/writes from internal/pos/inventory.go into pos repo methods; update callers.
- [X] T010 [P] [US1] Move pricing queries from internal/pos/pricing.go and corepos/pricing.go into pos repo methods; update both corepos and pos callers.
- [X] T011 [P] [US1] Move sales/shifts SQL from internal/pos/sales.go and shifts.go into pos repo methods; update callers.
- [X] T012 [P] [US1] Move inventory_api and shifts_api SQL (internal/pages/inventory_api.go, shifts_api.go) to pos repo methods; update handlers.
- [X] T013 [P] [US1] Move plugin host SQL (permissions.go, plugins.go, manifest.go, install.go, rollback.go, revocation.go, supervisor.go, update_checker.go, ipc.go, download_manager.go) into plugin repo methods; update plugin package callers.
- [X] T014 [P] [US1] Consolidate shared queries inside repos (helpers/builders) and remove duplicate SQL strings from callers to prevent drift.

## Phase 4: User Story 2 – Safeguard Transactions & Errors (P1)
- [X] T015 [P] [US2] Ensure repo methods accept caller-managed *sql.Tx; refactor transactional call sites (sales, inventory, plugin install/update/rollback) to pass tx without altering scope.
- [X] T016 [US2] Verify and align error mappings: repositories return errors consistent with pre-refactor handling; adjust wrapping/log points where needed.
- [X] T017 [US2] Re-run representative transactional flows (sale completion, inventory adjustment, plugin install/rollback) to confirm commit/rollback parity after repo moves.
- [X] T018 [P] [US2] Run performance regression check (benchmarks or targeted smoke) on representative flows to confirm <5% runtime delta post-refactor.

## Phase 5: User Story 3 – Observability & Tests Follow Data (P2)
- [X] T019 [P] [US3] Add/extend repo-level tests for moved queries (success/failure) across pos (catalog/inventory/pricing/sales/shifts), plugins, settings, and shortcuts repos; include caller integration assertions where behavior could change.
- [X] T020 [P] [US3] Ensure repo methods emit existing logging/metrics hooks; remove SQL-specific telemetry from handlers/plugins and rely on repo instrumentation.
- [X] T021 [US3] Run `go test ./...` and targeted smoke (sale/inventory/plugin path) to confirm handlers remain SQL-free and telemetry/logs still fire.
- [X] T025 [P] Sweep tests and mocks for inline SQL; wrap through repos/helpers or shared fixtures, updating expectations to keep existing behaviors green.
- [X] T026 [P] Add pagination/order regression checks for catalog/inventory queries to ensure limits and ordering are unchanged after repo moves (include targeted benchmarks or golden-result comparisons).

## Phase 6: Polish & Cross-Cutting
- [X] T022 [P] Update specs/001-sql-repo-refactor/quickstart.md with final repo locations and guidance to avoid inline SQL.
- [X] T023 [P] Sweep for remaining inline SQL via `rg "SELECT|INSERT|UPDATE|DELETE" internal corepos` (fail if any SQL outside internal/data); resolve stragglers and record outcome.
- [X] T024 Confirm no contract/API changes were introduced; note “no new contracts” validation in tasks.md status update; final check against success criteria (SC-001..SC-005).

## Dependencies & Execution Order
- Phase 1 → Phase 2 must finish before user stories.  
- US1 (centralize) must complete before US2/US3 validation tasks.  
- US2 depends on US1 repos being in place.  
- US3 (observability/tests) depends on repos and transactional alignment from US1/US2.

## Parallel Execution Examples
- In US1, run T005/T006/T007 (settings/index/buttons) in parallel with T008/T009/T010 (catalog/inventory/pricing) and T013 (plugins) as they touch different files.
- In US3, T017 tests can parallelize across repos; T018 telemetry cleanup can proceed once corresponding repo methods exist.
