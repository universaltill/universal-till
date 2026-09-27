# Review: serialize SessionBasketManager.SetConfig batches (ut-docs#2488)

**Date:** 2026-09-27
**Branch:** `fix/2488-setconfig-batch-serialize`
**Author:** Sonnet (Dev subagent). **Reviewer:** Opus 5.5 (fresh-context subagent).
**Scope:** `internal/pos/session_manager.go`, `internal/pos/session_manager_test.go`

## What shipped

`SessionBasketManager.SetConfig` takes a snapshot of the live sessions under
`m.mu`, releases the lock, and then calls each session's `SetConfig`. The lock
is released first so that it is never held across a plugin ask (ut-docs#2435).
The side effect was that two concurrent calls could interleave their writes
to individual sessions, which left live sessions on different final configs.

A new field, `setConfigMu`, now serializes whole batches (snapshot plus
dispatch) against each other:

- Whichever call acquires `setConfigMu` last wins on every session.
- Only `SetConfig` takes the lock.
- Lock order is `setConfigMu -> mu -> Service.mu`.
- `mu` is still released before any Service call, so Get, Create, BindTable
  and the rest are never blocked by it.

Test: `TestSessionBasketManager_SetConfig_ConcurrentCallsDoNotInterleave`
creates one stuck session with a slow charge-policy asker and 32 plain
sessions. It checks two things:

- While call 1 is parked in the ask, call 2 has not returned and has not
  written cfg2 to any session.
- After the asker is released, every session ends on cfg2.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The test's doc comment inverted the failure odds without the fix. It said the mid-flight check fails about 1/33 of the time; it actually fails about 32/33, because call 2 writes to every session ahead of the stuck one. | Fixed: comment rewritten. |
| 2 | nit | Flakiness. With the fix the test cannot wrongly fail, because a slow box only makes call 2 wait longer. Without the fix it wrongly passes only about 1/1000 of the time. | Accepted, no change. |
| 3 | nit | The comments said "last to finish" where "last to acquire" is correct, and the same explanation appeared in three places. | Fixed: one full explanation on the field, a one-line pointer in the type doc and the func doc, and the inline comment removed. |
| 4 | out of scope | Callers run `Engine.SetConfig`, then `KioskEngine.SetConfig`, then `SelfOrderSessions.SetConfig`, with nothing serializing across the three. Two concurrent saves can still leave the Engine on config A and the sessions on config B. | Filed as ut-docs#3085 (Backlog). |

Reviewer checks with no finding:

- **Deadlock / re-entrancy:** the three callers are `settings_page.go` ×2 (HTTP) and `init.go`'s `newRederiveSettings` (background re-apply). None is reachable from a plugin ask callback.
- **Stall bound:** a slow ask is bounded by the WASM per-call `context.WithTimeout` with `WithCloseOnContextDone` (`wasm_runtime.go`). A second concurrent save therefore waits at most sessions × ask timeout.

## Verification

- **TDD, re-verified independently by the reviewer in a detached worktree.** With the production change reverted, `-race -count=5` failed 5/5 runs, each at the mid-flight assertion (`session 3 already has Config() = cfg2 while call 1's batch is still in flight`). With it restored, `-race -count=20` passed.
- **Dev's own pre-fix run:** 5/5 failed. One of those runs got past the mid-flight check and failed at the final mixed-config assertion (`session 2's Config() = …1000… want …2500…`).
- **Test bug caught during TDD:** the first draft's cfg2 (`TaxRateBasisPoints: 2000`) equalled the factory default, which made the test pass whether or not the fix was present. It was changed to 2500.
- **Full gate:** `gofmt -l .`, `go build ./...`, `go vet`, `go test ./...`, `golangci-lint run ./...`, and every guard in `ci.yml`'s `build` job.
- **Not run:** Playwright e2e and a driven run. There is no UI or runtime surface; this is an internal lock change only.

## Verdict

Safe to merge.
