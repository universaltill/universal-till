# Code review: shutdown races EventBus.publish → panic: send on closed channel

**Card:** universaltill/ut-docs#503
**Date:** 2026-08-09
**Complexity:** medium — Dev inline (Sonnet, self-discovered and fixed
directly by the orchestrating pipeline cycle, not delegated — see "How
this was found" below), Review via an independent Opus subagent (isolated
worktree). One review round: found no blockers, so no second round per
this pipeline's process-depth rule.

## How this was found

While working ut-docs#380 (a separate, independently-scoped shutdown-drain
card) in this same cycle, an overlapping concurrent pipeline cycle
completed and merged a *different* fix for the same card
(universaltill/universal-till#268, closing #380) before this cycle's own
work landed — a real instance of the two-hourly-lane collision this
pipeline's board accepts as a residual race. Rather than duplicate that
already-shipped work, this cycle's own independent review process (two
rounds of Opus review on the now-superseded parallel branch,
`pipeline/380-shutdown-drain-supervisor-wasm-v2`, kept as reference) had
already surfaced a blocker-class concurrency bug in *that* approach's
wiring shape. Checking whether the actually-merged #268 shared the same
defect confirmed it did — reproduced directly against `main` (3e28e37),
not hypothetically. This card is that fix, built fresh against current
`main` rather than trying to reconcile two independent branches.

## What shipped

`internal/server/server.go`'s graceful-shutdown goroutine (registered on
`app.Run`'s shared `wg`, triggered by `<-ctx.Done()` where `ctx` is
`app.Run`'s `bgCtx`) called `pluginManager.Close(shutdownCtx)` — added by
PR#268 to join the wasm runtime's per-plugin event-channel drainer
goroutines, closing ut-docs#380. `Manager.Close` → `WasmRuntime.Close` →
`SharedBus(db).ResetSubscribers()`, which closes every open subscriber
channel.

The bug: that shutdown goroutine's trigger (`bgCtx.Done()`) is the SAME
cancellation signal that independently triggers every OTHER background
service on the same `wg` — `internal/cloudsync.Start`'s ticker included.
`srv.Shutdown` only drains in-flight HTTP requests; it does nothing for an
independent background goroutine like `cloudsync`'s. `EventBus.publish`
(`internal/plugins/ipc.go`) snapshots its subscriber slice under `RLock`,
releases the lock, does a per-subscriber `CheckPermission` DB query, and
only then sends on the channel — closing that channel inside that window
is `panic: send on closed channel`, unrecovered in `cloudsync`'s
goroutine (no `recover()` there), meaning a full process abort with
in-flight SQLite work abandoned.

**Reproduced directly against `main` (3e28e37)**: a tight-loop publisher
racing 300 subscribe/`Manager.Close` cycles through the real production
call path (`SharedBus(db).ResetSubscribers()`, exactly what `Manager.Close`
invokes) panics reliably, typically within the first few iterations.

Fix:

- **`internal/server/server.go`**: removed the `pluginManager
  *plugins.Manager` parameter from `Start` entirely, and removed the
  `pluginManager.Close(shutdownCtx)` call from the `<-ctx.Done()`-triggered
  shutdown goroutine. Doc comment explains why this call site is
  deliberately gone, not just missing.
- **`internal/server/server_test.go`**: updated the 3 call sites that
  passed an extra `nil` for the removed parameter.
- **`internal/app/app.go`**: `pluginManager` is now predeclared (`var
  pluginManager *plugins.Manager`) before the `defer func(){ stopBg();
  drainBackgroundServices(...); ...; pluginManager.Close(closeCtx) }()`
  block, so the closure can reference it before `plugins.Init` assigns it
  later in the function (assignment changed from `:=` to `=`
  accordingly). `pluginManager.Close` now runs from this deferred closure,
  bounded by a new `wasmCloseTimeout` (5s), strictly AFTER
  `drainBackgroundServices` completes — by which point every `wg`-joined
  publisher (`cloudsync` included) has actually exited, not merely been
  asked to.
- **`internal/pages/init.go`**: doc comment updated to describe the
  corrected sequencing.
- New test: **`internal/plugins/manager_close_sequencing_test.go`** — a
  300-iteration loop using the real `SharedBus`/`Manager.Close` call path:
  each iteration runs a live publisher goroutine, stops and drains it
  (mirroring `drainBackgroundServices`), THEN calls `Close` — proving the
  fixed (post-drain) sequencing never panics.

## Independent review (Opus, isolated worktree)

Reproduced the panic independently against the real production call path,
then proved the new test has teeth by moving its drain lines (`close(stop);
publisherWg.Wait()`) to *after* `m.Close` (i.e. the pre-fix sequencing) —
panicked immediately, confirming the test's assertions are load-bearing,
not incidental.

Nearly rejected the card's core premise on a first pass (found no obvious
shared-`wg` background publisher), then chased the indirection and
confirmed it: `cloudsync.Hooks.AdjustStock` → `cloudAdjustStock`
(`internal/pages/cloudsync_wire.go`) → `publishStockAdjusted`, running on
cloudsync's wg-registered ticker goroutine, not an HTTP handler — so
`srv.Shutdown` never covered it. The card's premise holds.

Independently verified nil-safety across all three shapes (nil
`*Manager`, `&Manager{}` with a nil `Wasm` field, non-nil `Wasm` with a
nil `db`) rather than trusting the diff's own comment — confirmed this
fix does not reintroduce the bare-field-dereference class of bug found in
a different, now-superseded parallel attempt at the original #380 card.
Re-derived the sequencing from source: exactly one call site for
`pluginManager.Close` (`app.go`), textually after `drainBackgroundServices`
in the same closure, and confirmed via Go's defer/LIFO semantics that it
runs before `database.Close()` on every return path. Enumerated every
`wg`-registered background service in the app and confirmed each is
joined before `Close` runs.

**Verdict: safe to merge, no blockers.**

### Should-fix — filed as a follow-up card, not folded in

The identical hazard is reachable at *runtime*, not just shutdown:
`Manager.Reload` (a plugin install/uninstall, including a cloud-pushed
directive on the same `cloudsync` ticker) calls `WasmRuntime.Sync` →
`bus.ResetSubscribers()`, which can race an in-flight HTTP handler's
`publish` the same way. This diff doesn't touch that path — the root fix
belongs in `EventBus.publish`/`ResetSubscribers`'s lock-release-before-send
shape itself, which would close both cases at once, and needs its own
design decision first. **Filed as ut-docs#504.**

### Nitpicks — accepted as-is

- `srv.Shutdown`'s own 5s bound: if it expires, in-flight HTTP handlers
  can still be live when `Close` runs — no worse than before this fix,
  just not fully closed by it either. Narrower case of the same
  degraded-path caveat the code comments already document for
  `drainBackgroundServices`'s own timeout.
- Worst-case shutdown is now additive: `backgroundDrainTimeout` (10s) +
  `wasmCloseTimeout` (5s) = 15s. Typical cost is ~0 (drainers exit as soon
  as channels close); the tradeoff is documented in the code comments.

## Verified beyond the automated suite

- **Real production call path**, both by the author and independently by
  the reviewer: `SharedBus`, real `Subscribe`/`Publish`, real
  `Manager.Close` — not a mock or a synthetic `EventBus` instance.
- **TDD, both directions**: reverting the fix's sequencing (moving the new
  test's drain lines to after `Close`, i.e. simulating the pre-fix call
  order) panics immediately; restored, green again — done independently
  by both the author and the reviewer.
- **Nil-safety traced end to end**: `nil *Manager`, `&Manager{}` with a
  nil `Wasm`, non-nil `Wasm` with a nil `db` — all confirmed
  non-panicking, by direct exercise, not assumed from the method's own
  `if x == nil` guard alone.
- **Full publisher inventory**: every `wg`-registered background service
  in `app.Run`'s boot sequence enumerated and confirmed joined by
  `drainBackgroundServices` before `pluginManager.Close` can run.
- **Blast radius of the `server.Start` signature change**: confirmed
  exactly 1 real caller (`app.go`) and 3 test call sites, all updated; no
  other reference to the removed parameter anywhere in the repo.
- Full `go build ./...`, `go vet ./...`, `gofmt -l .` (clean on every file
  this diff touches; drift elsewhere is the same 4 pre-existing files
  tracked separately, confirmed untouched), full `go test ./... -race
  -count=1` (all green, zero data races — run by both the author and the
  reviewer independently), and all 4 CLAUDE.md guards plus
  `guard-help-topics.sh` (this diff is Go-only, no templates/locales/
  routes touched).
- No user-facing strings, no `web/help/` update needed — confirmed
  directly (diff is Go-only: `internal/server/server.go`,
  `internal/server/server_test.go`, `internal/app/app.go`,
  `internal/pages/init.go`, plus one new `_test.go` file). No ADR needed —
  a bounded sequencing fix, not new architecture.

## Deferred / explicitly out of scope

- **ut-docs#504** — the identical hazard reachable via `Manager.Reload`
  at runtime (plugin install/uninstall), not just shutdown. Root fix
  belongs in `EventBus.publish`/`ResetSubscribers` itself.
- The `srv.Shutdown`-bound nitpick and the additive-timeout nitpick above
  — both accepted as documented tradeoffs, not defects.

## Safe-to-merge verdict

Yes. Independently reproduced and independently re-verified fixed, by a
different model than the one that authored the fix, with all gates green.
No real client/shop name anywhere; no secret-shaped literal introduced.
