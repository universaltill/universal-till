# Code Review — Data-access policy guard + repository-pattern fix

Date: 2026-07-07
Scope: `scripts/ci/guard-data-access.sh` (new), `.github/workflows/ci.yml`,
`internal/data/plugin_repo.go`, `internal/plugins/installer_marketplace.go`,
`CLAUDE.md`.

## Intent
Mechanically enforce the repository pattern (docs `reference/coding-standards.md`
§1): raw SQL must live only in `internal/data` (repositories) and `internal/db`
(migrations). A blunt depguard on `database/sql` is wrong here — the POS
legitimately threads `*sql.Tx`/`*sql.DB` through the domain layer — so the guard
targets SQL **query text**, not the import.

## Guard
`guard-data-access.sh` greps `internal/**.go` for SQL statements (quoted/backtick
same-line, or a keyword at line start for multi-line queries), excluding
`internal/data`, `internal/db`, `internal/testsupport`, and `*_test.go`. Wired into
CI as a step before Build.

## Finding surfaced by the guard (fixed)
The guard caught a **real violation**: `installer_marketplace.go` ran an
`INSERT … INTO plugin_catalog … ON CONFLICT …` inline via `i.db.ExecContext`.
- **Fix:** moved the statement into a new `PluginRepo.UpsertCatalogEntry` +
  `CatalogUpsertRow` in `internal/data/plugin_repo.go` (co-located with the other
  `plugin_catalog` access); the installer now calls it. No behavior change — same
  SQL, same parameters, same conflict handling.
- `internal/testsupport/sqlite_catalog.go` (ephemeral schema for tests) is
  intentionally exempt, like test files.

## Verification (performed)
- `go build ./...` OK; `go vet ./internal/data/... ./internal/plugins/...` OK.
- `go test ./internal/data/... ./internal/plugins/...` → all pass (plugins incl.
  the full install suite).
- Guard: passes on the cleaned tree; **negative test** (injected
  `SELECT … FROM sales` in `internal/pos`) is correctly flagged.

## Findings
- No import cycle: `internal/plugins` already imports `internal/data` (10 files);
  `internal/data` does not import `plugins`.
- The gopls "not in workspace module" diagnostics are environment noise (no
  go.work); the real `go build`/`go test` are green.

## Disposition
Approved. Enforcement added and the one pre-existing violation it revealed is
properly fixed.
