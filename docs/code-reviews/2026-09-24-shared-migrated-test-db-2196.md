# Code review — shared pre-migrated SQLite template for internal/data tests (ut-docs#2196)

**Date:** 2026-09-24 · **Branch:** `perf/2196-shared-migrated-template` · **Reviewer:** independent Fable subagent (author: Opus 5.5)

## What shipped

- `internal/testsupport.MigratedDBFile(t, name)`: builds a fully migrated
  database once per test binary (`sync.Once`, in a `MkdirTemp` dir that is
  read into memory and removed), then writes a fresh copy into
  `t.TempDir()/name` per call and returns the path. Callers still call
  `db.Open(path)`, which finds every migration applied and only verifies the
  ledger — `Open`'s real behaviour is still exercised.
- All 102 `db.Open` call sites in 54 `internal/data` test files switched
  (98 `db.Open(filepath.Join(t.TempDir(), n))` → `db.Open(testsupport.MigratedDBFile(t, n))`,
  plus 4 `path :=`/`replicaPath :=` assignments).
- Guard `internal/data/migrated_template_guard_test.go`
  (`TestDataTestsUseMigratedTemplate`) fails on a new `db.Open(...TempDir()...)`
  in the package; escape hatch `// migrated-template:allow <reason>`.
- Test-only: no repository method or production file changed.

## Measurements (4-vCPU cloud container)

| | per call, plain | per call, `-race` |
|---|---|---|
| `db.Open` on an empty path (40 migrations) | 176 ms | 5.19 s |
| `db.Open` on a template copy | 6 ms | 122 ms |

`go test -count=1 ./internal/data/`: `main` **165.8 s** → branch **16.5 s**
(both runs overlapped, so both are contended; the reviewer measured 155.5 s → 16.8 s).
`-race`: see the close-out comment on ut-docs#2196.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Guard regex missed `db.Open(t.TempDir() + "/x.db")` | Fixed: regex widened to `db\.Open\([^)]*TempDir\(\)`; a probe file with the concatenation form fails the guard. Paths built into a variable first are still not caught (documented in the guard's comment). |
| 2 | minor | `sync.Once` failure is sticky | Accepted, documented: it can only fail if migrations are broken, which fails every such test anyway. |
| 3 | nit | `name` with a subdirectory would ENOENT | Fixed: `os.MkdirAll(filepath.Dir(path))`. |
| 4 | nit | Copy is mode 0600 vs driver-created 0644 | Accepted: no test inspects the mode (grepped); matches `internal/db/lock.go`. |
| 5 | nit | `os.ReadFile` error unwrapped | Fixed. |

## Verified beyond the unit tests

- Reviewer compared a template copy with a fresh `db.Open`: `PRAGMA integrity_check`
  ok, WAL mode persisted, no `-wal`/`-shm` sidecars, identical `sqlite_master`
  SQL and identical row counts in all 93 tables (seed rows, `schema_lineage`,
  ledger checksums carry over). `migrate()` on a copy applies nothing; the 028
  backfill post-check runs only inside `applyMigration`.
- The 4 path-variable sites only reuse the path for a second raw connection
  after `db.Open`, which is the same as before.
- Isolation: `TestMigratedDBFile_FullyMigratedAndIsolated` (a write in copy A is
  not visible in copy B); `go test -count=2 -shuffle=on ./internal/data/` passes;
  `-race` on `internal/testsupport` passes.
- TDD re-verified by the reviewer: without `migrated_db.go` the helper test fails
  (`undefined: MigratedDBFile`); restoring `tables_repo_test.go` from `main`
  fails the guard at `tables_repo_test.go:28`.
- `internal/testsupport` now imports `internal/db`; only `_test.go` files import
  `testsupport`, so no production binary gains the dependency.
- `guard-data-access.sh` and the other CI `build` job guards pass; golangci-lint
  0 issues on the changed packages. (`guard-shellcheck-version.sh` cannot run
  here: there's no `shellcheck` binary in the container, and the diff touches
  no shell scripts.)

## Deferred

- 11 test files in other packages still use `db.Open(filepath.Join(t.TempDir(), …))`,
  outside this card's `internal/data` scope. Filed as a Backlog card.
- The `-timeout 60m` in `make test-race-data` stays (per the card).

## Verdict

Safe to merge.
