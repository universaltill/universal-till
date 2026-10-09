# Review: the #1718 interrupt guard failed on main from a TempDir cleanup race (ut-docs#4000)

Branch `fix/4000-interrupt-test-cleanup`. Author: Opus 5.5 (lane:cloud-41,
complexity:medium). Independent review: Fable, fresh context, in a separate
worktree.

## What happened

- On `main` at `96038a9`, `TestCancelledQueryNeverInterruptsAnotherGoroutine`
  (`internal/db/interrupt_race_test.go`) failed once. Run 37897516913,
  attempt 1. A re-run passed.
- The job log has exactly one failure line:
  `testing.go:1619: TempDir RemoveAll cleanup: unlinkat …/001: directory not empty`.
  No #1718 assertion failed: the victim goroutine never saw SQLITE_INTERRUPT,
  and enough queries ran.

## Root cause

- `sql.DB.Close` does not wait for database/sql's background
  `connectionOpener`. An `openNewConnection` already in `Connect` finishes
  that open, sees `db.closed`, and closes itself after `Close` has returned.
  modernc's `Connect` checks the context only before `sqlite3_open_v2`.
- This test produces those background re-opens by design:
  - modernc `conn.usable()` reports an interrupted file connection as
    `driver.ErrBadConn`, so the pool discards it.
  - `finalClose` then queues a re-open for the 8 cancellers waiting on
    `MaxOpenConns=2`.
- So `-wal`/`-shm` can still be created or removed after `Close`, and
  `t.TempDir`'s single `RemoveAll` loses the race.
- Evidence:
  - Author's probe: `-wal`/`-shm` were present right after `Close` in 3/8
    runs and gone 200 ms later.
  - Reviewer's probe: a live `openNewConnection` goroutine after `Close` in
    7/8 runs; its files vanished 9–18 ms later.
- No production consequence: `ApplyPendingRestore` removes `-wal`/`-shm`
  only before `Open`, with no live pool.

## What shipped (test-only)

- The guard test keeps its database in an `os.MkdirTemp` directory. Its
  `t.Cleanup` calls a new test helper, `removeAllEventually(dir, 10s)`, which
  retries `RemoveAll` every 20 ms and returns the last error once the
  deadline passes. A directory that never empties still fails the test,
  through `t.Errorf` in the cleanup. A genuinely leaked connection would show
  up there.
- `d.Close()` now runs in a defer with an error check. Defers run before
  cleanups.
- New `internal/db/interrupt_cleanup_test.go`:
  - `TestRemoveAllEventuallyOutlastsALateWriter`: a hot writer keeps
    creating files for ~300 ms. The helper must remove the directory.
  - `TestRemoveAllEventuallyGivesUpAfterItsDeadline`: a NUL-byte path makes
    every attempt fail. The helper must return the error, and only after its
    deadline.
- Nothing the #1718 guard asserts changed. It is not skipped, quarantined or
  weakened.

## TDD

- With the helper stubbed as a one-shot `os.RemoveAll`, both helper tests
  failed 5/5:
  - late writer: `unlinkat …: directory not empty`, the CI error;
  - deadline: `gave up after 13µs, before its 200ms deadline`.
- With the real helper, both pass. The reviewer re-verified this
  independently in its worktree, with the same outputs.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | Comments didn't say *why* a background re-open happens. | Fixed: names the ErrBadConn → `finalClose` re-open path. |
| 2 | nit | Late-writer test's goroutines could outlive a `Fatalf` by ~300 ms. | Fixed: deferred `stop` + `wg.Wait()` on every exit path. |
| 3 | nit | "at most MaxOpenConns late opens" is an upper bound. | Reworded to "bounded by MaxOpenConns". |
| 4 | nit | 2 s slack on the deadline test's upper bound. | Accepted: an anti-hang bound, generous for loaded runners. |

## Verification

- `gofmt -l .`: no output. `go build ./...`, `go vet`: clean.
- `golangci-lint run ./...`: 0 issues. This used a go1.27-built v2.14.0,
  because the container's go1.25 build refuses the repo (ut-docs#4001).
- Full `go test ./...`: green.
- 15× `-race` run of the guard and helper tests while the full suite ran
  alongside for CPU load: green. The reviewer ran 3× `-race`: green.
- `ci.yml` `build` guards: all pass except two environmental ones. The
  container has no `shellcheck` binary, and `retry-with-backoff.sh` is a
  helper that needs arguments. Neither concerns this diff.
- No UI or runtime surface, so no driven run or e2e applies.

## Verdict

Safe to merge.
