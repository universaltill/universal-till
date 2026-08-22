# internal/pages test package: remove fsync from the shared test-DB hot path (ut-docs#878)

## What shipped

`internal/pages`'s test binary hung twice on unrelated PRs
(universal-till#425, #429), killed at `go test`'s default 10-minute
per-package timeout with a goroutine dump showing `syscall.Syscall6`
stuck deep inside `modernc.org/sqlite`'s `fsync` call, reached via
`seedForPages` through the package's shared test-DB helper
`openPagesTestDB`. Both times a bare re-run of the same commit (no code
change) came back fully green.

Root cause: `openPagesTestDB` (and a second, separately-defined helper,
`newSyncDepsWithPath` in `sync_api_test.go`) opened its temp-file SQLite
DB with the driver's default rollback-journal mode, which does a real
`fsync` disk syscall on every commit. Both DBs are single-connection
(`SetMaxOpenConns(1)`/`SetMaxIdleConns(1)`) and live only in
`t.TempDir()` for the duration of one test — there is nothing durable to
protect against a crash, so every one of those fsyncs was pure,
avoidable exposure to CI-runner disk-I/O contention.

Fix: both helpers now set `PRAGMA journal_mode = MEMORY` and
`PRAGMA synchronous = OFF` right after the existing `busy_timeout`/
`foreign_keys` pragmas, keeping the rollback journal off disk entirely
and skipping the sync. A third file-backed test DB in the same package,
`import_bkp_page_test.go`'s `buildBkpDBBytesForPagesTestWithTaxRows`
(reads its DB back as raw bytes after `db.Close()`), had the identical
exposure — added during review, not implicated in either crash, but the
same class and cheap to close.

- `internal/pages/ui_smoke_test.go`: `openPagesTestDB` gets the two
  pragmas; new regression test `TestOpenPagesTestDB_NoFsyncOnHotPath`
  asserts `journal_mode = 'memory'` and `synchronous = 0` on the DB it
  returns.
- `internal/pages/sync_api_test.go`: `newSyncDepsWithPath` gets the same
  two pragmas (same single-connection, `t.TempDir()`-scoped shape;
  `db.Snapshot`'s `VACUUM INTO` runs against the same `*sql.DB` and is
  unaffected by the source's journal mode).
- `internal/pages/import_bkp_page_test.go`:
  `buildBkpDBBytesForPagesTestWithTaxRows` gets `SetMaxOpenConns(1)` +
  the same two pragmas, closing the last file-backed exposure in the
  package.

## TDD

Wrote `TestOpenPagesTestDB_NoFsyncOnHotPath` first. Confirmed it fails
against the unmodified helper with the actual pre-fix values:

```
ui_smoke_test.go:81: journal_mode = "delete", want "memory" (...)
ui_smoke_test.go:89: synchronous = 2, want 0 (...)
```

then implemented the pragma change and confirmed green.

## Independent review

Spawned via `Agent` at `model: opus` (this card is `complexity:medium`,
Sonnet built it) in an isolated worktree. **Verdict: PASS, safe to merge
as-is.**

The reviewer independently re-verified the TDD claim (revert pragmas →
RED with the same `delete`/`2` values → restore → GREEN), independently
reproduced the performance claim on their own run of the full stack
(174.1s wall / 49.1s sys without the fix vs 73.5s wall / 16.2s sys with
it — user time essentially unchanged, which is the fsync-removal
signature, not work skipped), confirmed `go build`/`go vet`/
`gofmt -l .`/the affected package's `go test` and both
`guard-data-access.sh`/`guard-i18n.sh` guards all pass, and grepped every
`sql.Open("sqlite", …)` call site in the package to confirm coverage
(7 are `:memory:` with no fsync exposure at all; 2 are read-only fixture
DBs with no commits to fsync; the two originally-fixed helpers and the
one the review surfaced accounted for the rest).

Two non-blocking findings, both fixed before merge:
- **F1**: `import_bkp_page_test.go`'s file-backed fixture DB had the same
  unfixed exposure — fixed above.
- **F2**: the new regression test didn't close its DB (`defer db.Close()`
  missing, inconsistent with the other 9 call sites in the file) — fixed.

One informational note accepted as-is, not actioned: `journal_mode =
MEMORY` is per-connection (unlike WAL, not persisted in the file header),
so it would silently stop applying if either helper's pool size were
ever raised above 1 in the future. Both helpers pin the pool to 1
immediately above the pragmas today; flagged for whoever touches this
next rather than guarded against now.

The reviewer's own honest caveat, carried forward here: this fix removes
a real, measured, expensive syscall from the hot path and the stack-
trace evidence favors "slow fsync under I/O contention" over a Go-level
deadlock (a kernel-syscall park looks nothing like a mutex/channel
park) — but an intermittent CI-runner hang can't be proven absent from a
local worktree. Per the acceptance criteria's own explicit allowance
("root-caused and fixed, or documented as unreproducible... mitigation
exists so a future occurrence fails faster and/or stops happening"),
this is the concrete mitigation: the specific syscall in both crash
traces no longer executes on this path at all.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `go test ./internal/pages/... -count=1` — green, ~74s (down from ~175s
  pre-fix on this machine).
- Full `go test $(go list ./... | grep -v '/internal/plugins$')` — green,
  no other package affected (the changed helpers are package-private).
- `scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh` — pass.
- No UI surface, user-facing string, or manual/help-topic touched
  (test-only diff, confirmed from the diff stat, not assumed).
- No real client/shop name or secret-shaped literal introduced.

## Deferred / explicitly out of scope

- Not shortening the package's CI timeout — the acceptance criteria
  allows "fails faster and/or stops happening"; this fix targets the
  latter, and cutting the timeout without real CI-runner timing data
  risked introducing new flakiness rather than removing it.
- Confirming from real GitHub Actions logs (rather than the issue's own
  quoted trace) — this session doesn't have Actions log access; DevOps
  will confirm the next few `main` merges stay clear of this signature.
