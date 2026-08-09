# Code review: shutdown drain for Supervisor.monitorProcess and the wasm event-channel drainer

**Card:** universaltill/ut-docs#380
**Date:** 2026-08-09
**Complexity:** hard — Dev via a Fable subagent (isolated worktree), Review
via independent Opus subagents (isolated worktree), per this pipeline's
model-routing rule. TWO full review rounds: round 1 found a blocker-class
concurrency bug (a shutdown-time panic risk), which is exactly the bar this
pipeline's process-depth rule sets for earning a second round. Round 2 found
that the fix for round 1's blocker introduced a second, distinct blocker
(a nil-pointer panic on an early-return path) — the orchestrator fixed both
personally and re-verified with the same rigor (an end-to-end repro of each
failure mode, before and after) rather than spinning a third full round,
since both fixes were small, mechanical, and independently reproducible.

## What shipped

`app.Run`'s shutdown drain (`internal/app/app.go`) had two goroutine classes
its own doc comment named as "still not covered" since the drain was first
built: `internal/plugins.Supervisor`'s `monitorProcess` goroutines (native
plugin process supervision — spawned per start and per restart) and
`internal/plugins.WasmRuntime`'s per-plugin event-channel drainer goroutines
(spawned by `Sync` for non-blocking-mode event subscriptions). Neither was
joined before this card: a `Supervisor.Shutdown` call returned immediately
after cancelling each process's context, while `monitorProcess` could still
be blocked in `Cmd.Wait()` and then write an audit row after the DB was
closing; the wasm drainers' channels were only ever closed by the *next*
`Sync`/reload's `ResetSubscribers` call, never at actual process shutdown,
so they leaked forever on every real shutdown.

- **`internal/plugins/supervisor.go`**: `Supervisor` gained an internal
  `procWg sync.WaitGroup` (`Add(1)` immediately before every
  `go s.monitorProcess(...)` — both call sites already hold `s.mu`, the
  same lock `Shutdown` uses to flip `stopped`/cancel, so an `Add` can never
  race a `Wait` that's already begun) and a `closed bool` guard (set under
  `s.mu` at the top of `shutdown`'s critical section; `StartPlugin` checks
  it under the same lock and refuses to spawn a new monitored process after
  shutdown — closing the one gap `WasmRuntime`'s equivalent guard already
  had). `Shutdown`/`shutdown` now releases `s.mu` and waits — bounded by
  `pluginShutdownDrainTimeout` (4s) or the caller's own `ctx.Done()`,
  whichever comes first — for every `monitorProcess` goroutine to actually
  exit, logging loudly (not silently) on either bound.
- **`internal/plugins/wasm_runtime.go`**: `WasmRuntime` gained `drainWg`
  (tracking the per-plugin drainer goroutines `Sync` spawns), `bus` (the
  last-seen `EventBus`, so `Shutdown` can close its subscriptions), and a
  `closed` guard mirroring the pattern above. `Shutdown(timeout)` closes
  every subscriber channel via `EventBus.ResetSubscribers()` and waits
  (bounded) for the drainers to exit.
- **`internal/app/app.go`** / **`internal/pages/init.go`**: the two doc
  comments that named these gaps as still-open are corrected. `Supervisor`'s
  join is folded transitively into the existing `wg` chain (it already runs
  inside `server.Start`'s wg-joined shutdown goroutine). The wasm join is
  **not** wired as a `wg` member racing `bgCtx.Done()` the way every other
  background service here is — see "What round 1 found" below for why —
  and instead runs from `app.Run`'s own deferred cleanup, strictly after
  `drainBackgroundServices` completes.
- New tests: `internal/plugins/supervisor_shutdown_test.go` and
  `internal/plugins/wasm_shutdown_test.go` — both a "the join is real" case
  (assert the goroutine's own side effect, not just that `Shutdown`
  returns) and a "wedged goroutine, bounded timeout, logs loudly" case per
  class, TDD'd in both directions.

## Independent review, round 1 (Opus, isolated worktree)

Found the join logic itself correctly built, but **one blocker**: the
initial wiring made `Wasm.Shutdown` a `wg.Add`-registered goroutine that
fired on `bgCtx.Done()` — the same signal `srv.Shutdown`'s graceful drain
and every other background service race to exit on. `Wasm.Shutdown`'s
`ResetSubscribers()` call closes the exact subscriber channels
`EventBus.publish` sends on, and `publish` releases its read lock before
that send (a per-subscriber `CheckPermission` DB query sits in between) —
so closing those channels while a publisher (an in-flight checkout,
`cloudsync`'s **unrecovered** background goroutine) could still be mid-send
is a real `panic: send on closed channel`, reproduced directly (300
subscribe/reset cycles against a live publisher, panicked in 0.067s).
Unrecovered in `cloudsync`'s path, this is a full process abort with
in-flight SQLite work abandoned — worse than the leak this card set out to
fix.

Also found 6 should-fix items (S1–S6): the wasm join test didn't actually
prove the join (deleting the `drainWg` wiring entirely left it green); both
timeout tests filtered logs without anchoring to the test's own start time
(false-fail/false-pass risk under `-count=N`); `Supervisor.shutdown` ignored
its own `ctx`, understating how long it could actually take stacked behind
`server.Start`'s `srv.Shutdown`; `Supervisor` lacked the `closed` guard its
wasm sibling had; two doc comments overclaimed what a *timeout* branch
actually proves; and a `Sync`-loses-race-to-`Shutdown` edge case (traced as
benign) wasn't documented.

**Fix**: `Wasm.Shutdown` moved out of the `wg`/`bgCtx` chain entirely and
into `app.Run`'s deferred cleanup, called *after* `drainBackgroundServices`
— by which point every `wg`-joined publisher (`cloudsync.Start`,
`server.Start`'s shutdown goroutine) has actually exited, not merely been
asked to. All 6 should-fix items folded in and independently re-verified
(direct mutation of the exact regression each guards against — see the
TDD section below).

## Independent review, round 2 (Opus, isolated worktree, scoped to the fix)

Confirmed round 1's blocker genuinely fixed (reproduced the *old* wiring's
panic 3 times against a live publisher; the *new* wiring survived 200
iterations × 8 publishers with zero panics) and all 6 should-fixes verified
fixed by direct mutation/probe — **but found the fix itself introduced a
second blocker**: `pluginManager.Wasm.Shutdown(...)` in the new deferred
cleanup dereferences a nil `*plugins.Manager` on every one of
`plugins.Init`'s four error-return paths, because `Wasm` is a *field*, not
a method — the field selector dereferences the nil `Manager` before
`WasmRuntime.Shutdown`'s own nil-receiver check ever runs. Reproduced
end-to-end through a real `app.Run` boot (a deliberately half-corrupt DB:
`schema_migrations` says applied, `plugin_catalog` table dropped) — a real
panic out of `Run`, which `mobile/mobile.go`'s unrecovered
`newInst.err = app.Run(ctx)` goroutine would let kill the host process,
directly contradicting that package's own "never `os.Exit` out from under
its own app" contract. Also flagged that `WasmRuntime.Shutdown`'s doc
comment (and `wasmShutdownDrainTimeout`'s) still described the *old*,
just-removed `wg`/`bgCtx` wiring.

**Fix**: `if pluginManager != nil { pluginManager.Wasm.Shutdown(...) }` in
the deferred cleanup; corrected the now-stale doc comments in `app.go` and
`wasm_runtime.go`'s `Shutdown` to describe the actual (fixed) sequencing,
including the honest caveat that `wasmShutdownDrainTimeout` is additive on
top of `backgroundDrainTimeout` (it now runs strictly after that drain),
not contained inside it, and that a `drainBackgroundServices` timeout can
reopen the original panic window on that degraded path (a residual,
should-fix-class risk noted by round 2, not itself blocker-class since it
requires two independent bounds to both already be exhausted).

Applied and personally re-verified — not spun into a third full
independent round, since this was a small, mechanical, directly
reproducible fix of the same class round 2 already demonstrated its own
repro technique for:
- End-to-end repro of the exact failure round 2 found: a temporary test
  booted a real `app.Run` against a DB with `plugin_catalog` dropped.
  Before the fix this would have panicked (verified the panic's mechanism
  independently: `Wasm` is a struct field, so `pluginManager.Wasm` on a nil
  `pluginManager` panics before `WasmRuntime.Shutdown`'s own `if w == nil`
  guard is ever reached); after the fix, `Run` returns cleanly with
  `"load plugin catalog: plugin.list_catalog: SQL logic error: no such
  table: plugin_catalog"` and no panic.
- Full gate re-run clean (see below).

## Verified beyond the automated suite

- **TDD, both goroutine classes, both directions**, re-verified personally
  after every round's fixes: for each of the four new tests, reverted just
  the production fix it targets, confirmed the exact expected failure
  (not a compile error), restored, confirmed green. Also independently
  reran round 1's own S1 mutation (delete `drainWg`'s wiring from `Sync`
  entirely) against the *current* test and confirmed it now fails with
  "Shutdown returned before the drainer's in-flight event was actually
  handled — the goroutine was not joined" — proving the strengthened wasm
  test (round 1's S1 fix) actually catches the regression it was written
  for.
- **Repeat-run stability**: both timeout-branch tests (`Supervisor` and
  `WasmRuntime`) pass under `-count=2` — the exact class of false-fail
  round 1's S2 finding demonstrated on the pre-fix log-filtering.
- **`ctx`-aware bounded wait**: `Supervisor.shutdown` given an
  already-cancelled context and a wedged `procWg` returns in ~126µs (its
  new `case <-ctx.Done()` branch), not the full `drainTimeout`.
- **Post-shutdown refusal**: `Supervisor.StartPlugin` called after
  `Shutdown` returns a `"supervisor is shut down"` error rather than
  spawning an unjoined `monitorProcess` goroutine.
- **Real wasm dispatch, not a mock**: the wasm join test installs a real
  compiled wasm guest module, publishes a real non-blocking event through
  the real `EventBus`, and asserts `Shutdown` did not return until the
  guest's `storage_set` host call had actually landed — checked the
  instant `Shutdown` returns, not after a subsequent poll.
- **Nil-pointer repro, end-to-end**: see round 2's fix section above — a
  real `app.Run` boot against a genuinely broken plugin catalog table, both
  before (panic, mechanism independently confirmed) and after (clean error
  return) the fix.
- Full `go build ./...`, `go vet ./...`, `gofmt -l .` (clean on every file
  this diff touches — the 4 flagged elsewhere, `internal/pages/
  external_api_test.go`, `internal/pages/import_page_test.go`,
  `internal/plugins/marketplace/client.go`, `internal/thirdparty/
  webview_go/webview.go`, are pre-existing drift confirmed untouched by
  this diff), full `go test ./... -race -count=1` (all green, zero data
  races, both independent reviewers and the orchestrator ran this
  separately), and all 4 CLAUDE.md guards (`guard-data-access`,
  `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-i18n`).
- No user-facing strings, no `web/help/` update needed — confirmed
  directly (no new page route, `git status --short -- web/` shows zero
  changes; the only new strings are two `logging.L().Errorf` operator-log
  messages, never rendered to a user) rather than assumed. No ADR needed —
  bounded engineering closing an already-documented gap, not new
  architecture.

## Deferred / explicitly out of scope

- The pre-existing hazard `EventBus.publish`'s lock-release-before-send
  shape represents (round 1: holding `RLock` across the non-blocking send
  would close this for every caller, not just the shutdown path this card
  touches) — flagged by both review rounds as its own follow-up, not this
  card's to fix.
- `Sync` itself calling `bus.ResetSubscribers()` mid-life (a plugin
  install/uninstall reload) races a live HTTP-handler publisher the same
  way shutdown used to — same underlying `EventBus` hazard, pre-existing,
  untouched by this diff, noted by round 2 as the same hazard class.
- The residual should-fix round 2 raised (a `drainBackgroundServices`
  timeout reopening the panic window on a doubly-degraded path) is noted
  in the corrected doc comments rather than re-architected — closing it
  fully means addressing the underlying `EventBus` hazard above, which is
  the right layer for that fix, not this shutdown-sequencing card.

## Safe-to-merge verdict

Yes. Both review rounds' blockers were fixed and independently
re-verified by direct reproduction of the exact failure mode each
described, in both the broken and fixed states. All 6 round-1 should-fix
items are fixed and re-verified. Full gate green. No real client/shop name
anywhere; no secret-shaped literal introduced.
