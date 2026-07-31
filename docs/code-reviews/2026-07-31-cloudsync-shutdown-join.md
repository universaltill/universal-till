# Code review — cloudsync.Start joins app.Run's shutdown drain (2026-07-31)

**Branch:** `fix/cloudsync-shutdown-join` (ut-docs#8, spun out of PR #101)
**Scope:** `internal/cloudsync/{cloudsync.go,cloudsync_test.go}`, `internal/pages/{cloudsync_wire.go,init.go}`, `internal/app/app.go`.

## What shipped

`cloudsync.Start` now takes a `*sync.WaitGroup` and registers its loop goroutine on it (same shape as `updates`/`alerts`/`enroll`), and — the load-bearing part — runs on **bgCtx**, not ctx: `app.Run`'s `stopBg()` cancels only bgCtx, so a wg-joined loop on ctx would never be signalled on early-error return paths and every shutdown would eat the full 10-second drain timeout. `pages.Init` gained explicit `bgCtx` + `wg` parameters (single production caller). The three existing `Start` tests replaced goroutine-count polling (`runtime.NumGoroutine()` — no happens-before edge, the exact gap the ticket documents) with real WaitGroup joins; a new `TestStartJoinsWaitGroupOnCancel` pins the join from inside the tick loop.

TDD: the join test was written first and confirmed failing (compile-stage, signature change) against HEAD.

## The race this kills — reproduced by the reviewer, worse than claimed

`go test ./internal/cloudsync/ -race -shuffle=on`: the independent reviewer reproduced the documented `issuereport.PendingDir` write-vs-`Tick` read race at HEAD at **5 failures in 12 runs** (the ticket estimated ~1 in 3). Post-change: reviewer 9/9 clean, pipeline 8/8 + 6/6 clean across sessions.

## Independent review (different model: Opus) — no blockers, 4 should-fix, 4 nits, all applied

The reviewer re-ran everything itself and went further:
- **Proved bgCtx is load-bearing** by flipping `StartCloudSync` back to ctx in a throwaway worktree: `TestRun_JoinsBackgroundGoroutinesOnEarlyServerError` fails at the 10s drain timeout — so the design decision has a real regression gate.
- **Confirmed no behavior traps**: no hook closure captures the outer ctx; all cloudsync HTTP uses `NewRequestWithContext` so drain aborts in-flight ticks immediately (the 30s client timeout never governs); grep across the whole ecosystem (incl. mobile/, GOOS-tagged files) found exactly one caller per changed symbol.
- **Should-fixes, all applied**: (1) `pages.Init`'s bgCtx comment stated the wrong reason — rewritten to name the actual mechanism and its gating test; (2) the comment implied a rule its three neighbours violate — now names `StartSyncPush`/`StartSyncPull`/`StartEODScheduler` as still-unjoined DB writers, tracked as **ut-docs#153**; (3) `app.Run`'s wg comment claimed completeness — now lists everything still outside the drain; (4) the tests' failure paths re-introduced the leak (a `t.Fatalf` before `cancel()` left a 5ms-ticking goroutine + shortened intervals for the rest of the package) — restructured with `t.Cleanup` joins (`startJoined` helper) so cancellation+join are unconditional, and interval restores run only after the join proves exit.
- **Nits applied**: dangling comment reference to the deleted `waitGoroutineExit`; the new join test now exercises the *inner* select branch (waits for a real tick before cancelling) instead of duplicating the early-cancel test; dead "baseline" comments removed; `CloseIdleConnections` removal verified harmless.

## Verification

- `go build ./...`, `go vet`, gofmt clean on all five files (both sides re-ran).
- `-race -count=3 -run TestStart` 12/12; `-race -shuffle=on` clean 6/6 after the hygiene restructure (and 9/9 by the reviewer pre-restructure).
- Full `go test ./...` green.
- Real shutdown behavior: bounded by existing `app` tests (early-error join gate passes in <3s, not the 10s timeout).

## Verdict

**Safe to merge.** Mechanism correct, the one architectural choice (bgCtx) independently proven necessary and regression-gated, and the documented race demonstrably gone under the exact invocation that reproduced it.
