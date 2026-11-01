# Review — bearer-rotation hook restore waits for a running rotation (ut-docs#5236)

**Date:** 2026-11-01 · **Branch:** `fix/5236-bearer-rotate-cleanup-race` · **Lane:** `lane:cloud-41`
**Author:** Opus 5.5 (inline) · **Reviewer:** Sonnet 5.5 (independent subagent, fresh context)

## What shipped
- `internal/lantls/peer.go`: the restore func `SetBearerRotate` returns now
  swaps the previous hook back **and then waits** until no rotation goroutine
  is running. A counter and a `sync.Cond` track the running goroutines.
  `fireBearerRotateOpt` re-reads the hook and counts the run under the same
  mutex (`bearerRotateRunStart`). A restore therefore either waits for a run
  or makes that run see the restored hook. A queued forced re-run is counted
  before the run that queued it finishes.
- Tests: `TestSetBearerRotate_RestoreWaitsForRunningHook`, and a
  `releaseAll` cleanup in `installRotateHook` so a parked hook never makes
  the restore hang.

## Why
`pages.TestSyncBearerFallback_TransientNonAuth401DoesNotLockOut` turned
`main` red (`pages-shuffle`). After the final fallback adoption, the forced
background rotation wrote `secrets/sync-bearer` while `t.TempDir()` cleanup
was removing that directory. The same window exists in production:
`app.go` removes the hook "before the database closes", but a rotation
already running could still write after that point.

## Verification
- TDD: the new lantls test failed before the fix ("restore returned while
  the hook was still running") and passes after it.
- Reproduction of the card's failure, 300 runs of the flaky test each:
  - unfixed `origin/main` (separate worktree): 3 of 300 failed with
    `TempDir RemoveAll cleanup: unlinkat …/secrets: directory not empty`;
  - with the fix: 300 of 300 passed.
- `go test -race ./internal/lantls` passes, as do the Bearer tests 50 times
  with `-race -shuffle=on`.
- `go test -race -count=3 -run TestSyncBearer ./internal/pages` passes
  (run by the reviewer).
- `gofmt` and `go vet` are clean, `go build ./...` passes, and
  `golangci-lint` reports 0 issues.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Shutdown can be delayed by up to `bearerRotateTimeout` (20 s) if a rotation is in flight to an unreachable main till | Accepted: the bounded wait is what keeps the write off a closed database. Documented in the `SetBearerRotate` comment and in ut-docs `architecture/lan-sync.md`. |
| 2 | nit | Calling restore from inside the hook would self-deadlock | Fixed: doc warning added. No caller does this. |
| 3 | nit | The first `bearerRotate.Load()` in `fireBearerRotateOpt` is now only a fast path | Fixed: comment added. |

The reviewer checked the following:
- no run can start after restore returns;
- `sync.Cond` is used correctly;
- the deferred done still runs on a panic;
- all four pages call sites register cleanups so the wait runs before
  TempDir, database and server teardown.

**Verdict:** safe to merge. No user-facing change; no help topic affected.
