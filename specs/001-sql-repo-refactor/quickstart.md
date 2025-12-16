# Quickstart: SQL Access Refactor to Data Repos

**Feature**: specs/001-sql-repo-refactor/spec.md  
**Date**: 2025-12-09

## Goals
- Remove inline SQL from business layers; route all DB access through `internal/data/*_repo.go`.
- Preserve transactions, error semantics, logging/metrics, and performance characteristics.

## Repo map
- POS/catalog/pricing/inventory/sales/shifts: `internal/data/pos_repo.go`, `internal/data/catalog_repo.go`.
- Plugins: `internal/data/plugin_repo.go`.
- Settings: `internal/data/settings_repo.go`.
- Shortcuts/buttons: `internal/data/shortcuts_repo.go`.

## Steps for Contributors
1) Identify SQL call sites in handlers/services/plugins (`rg "SELECT|INSERT|UPDATE|DELETE" internal`); confirm each is moved into the appropriate repo.  
2) If a flow uses transactions today, pass the transaction into repo methods rather than opening new ones.  
3) Move shared queries into helper functions inside repo files to avoid duplication; keep parameter ordering consistent.  
4) Update callers to use repo interfaces; remove dead SQL/DB handle imports from business layers.  
5) Add/extend repo-level tests (success/failure/transaction cases) and run `go test ./...`.  
6) Verify performance stability on representative flows (e.g., existing benchmarks or targeted path) and check logs for unchanged error messaging.  
7) Update docs/dev notes to remind contributors to avoid inline SQL in new code.

## Validation
- Lint: ensure no SQL strings remain outside `internal/data/`.  
- Tests: `go test ./...` with focus on moved repos and integration paths.  
- Smoke: exercise a representative sale/inventory/plugin flow to confirm transactional behavior unchanged.
- SQL sweep: `rg "SELECT|INSERT|UPDATE|DELETE" internal corepos` should only flag repo files and tests/fixtures.
