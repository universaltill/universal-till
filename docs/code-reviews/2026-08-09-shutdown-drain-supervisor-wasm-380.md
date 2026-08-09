# Code review: shutdown drain for Supervisor.monitorProcess and the wasm event-channel drainer

**Card:** universaltill/ut-docs#380
**Date:** 2026-08-09
**Complexity:** hard — Dev via a Fable subagent, Review via an independent
Opus subagent (isolated worktree), deliberately not Fable, per this
pipeline's model-routing rule (a model reviewing its own work shares its
blind spots). One review round: the round found one deterministic test
failure plus three lower-severity findings; the blocking one was fixed by
the reviewer in its own isolated worktree and pulled back in, none were
money/tax/data-loss/security class, so a second full independent round
wasn't earned per this pipeline's process-depth rule.

## What shipped

Two goroutine classes `app.Run`'s shutdown drain didn't cover, named
explicitly as the still-open remainder in that file's own comment since
ut-docs#153:

1. **`internal/plugins.Supervisor`'s `monitorProcess` goroutines.**
   `StartPlugin` spawned `go s.monitorProcess(...)` untracked, and
   `monitorProcess` self-respawns on every restart. `Supervisor.Shutdown`
   cancelled every process's context and returned without waiting for the
   corresponding monitor(s) to actually observe that, finish
   `proc.Cmd.Wait()`, and complete their own audit write —
   `database.Close()` in `app.Run`'s deferred cleanup could race a
   straggler still writing to `audit_log`.
2. **The wasm runtime's per-plugin event-channel drainer** (`internal/plugins/wasm_runtime.go`,
   started in `Sync` for non-blocking-mode hook events). Its channel is
   only closed by `EventBus.ResetSubscribers()`, called at the top of
   every `Sync`/`Reload` pass but never at real process shutdown — so the
   drainer blocked forever on `range ch` once nothing published any more
   events.

Fix, extending the existing `drainBackgroundServices` pattern
(`internal/app/app.go`) rather than inventing a new shutdown mechanism:

- **`internal/plugins/supervisor.go`**: `Supervisor` gained an internal
  `wg sync.WaitGroup`. `s.wg.Add(1)` precedes every `go
  s.monitorProcess(...)` call (the initial spawn in `StartPlugin`, and the
  self-respawn inside `monitorProcess`'s restart path); `defer
  s.wg.Done()` is `monitorProcess`'s first statement, covering all five
  return paths. `Shutdown` now releases `s.mu` **before** waiting on
  `s.wg` (required — `monitorProcess` re-acquires `s.mu` itself, so
  waiting under the lock deadlocks), bounded by the caller's `ctx`; on
  `ctx.Done()` it logs loudly via `logging.L().Errorf(...)` (mirroring
  `drainBackgroundServices`'s shape) and returns anyway rather than
  hanging shutdown forever on a wedged plugin process.
- **`internal/plugins/wasm_runtime.go`**: `WasmRuntime` gained its own
  `wg`; the drainer goroutine in `Sync` is now `Add`/`Done`-tracked. New
  `Close(ctx)`: nil-safe, closes every open subscriber channel via
  `SharedBus(db).ResetSubscribers()` (verified — see below — that in
  production only `WasmRuntime.Sync` subscribes on the shared bus, so this
  can't orphan another subsystem), then waits for `wg` bounded by `ctx`
  with the same loud-log-on-timeout shape.
- **`internal/plugins/plugins.go`**: new nil-safe `Manager.Close(ctx)` →
  `m.Wasm.Close(ctx)`.
- **`internal/server/server.go`**: `Start` gained a `pluginManager
  *plugins.Manager` parameter; its shutdown goroutine calls
  `pluginManager.Close(shutdownCtx)` right after `supervisor.Shutdown(shutdownCtx)`,
  reusing the same 5s `shutdownCtx` rather than a new timeout constant.
- **`internal/app/app.go`** / **`internal/pages/init.go`**: both doc
  comments that named these two classes as "STILL NOT covered" / "NOT yet
  joined" are rewritten to say they're covered now and how (via
  `Supervisor`/`WasmRuntime`'s own internal WaitGroups, not the shared
  `wg` those comments were originally about).
- New tests: `internal/plugins/shutdown_drain_test.go`
  (`TestSupervisor_Shutdown_WaitsForMonitorProcessGoroutines`,
  `TestSupervisor_Shutdown_AfterCrashRestartCycleDrainsCleanly`,
  `TestSupervisor_Shutdown_TimesOutLoudlyOnWedgedMonitor`) and additions
  to `internal/plugins/wasm_sync_test.go`
  (`TestWasmRuntimeClose_StopsDrainersAndReturnsPromptly`,
  `TestWasmRuntimeClose_NilSafe`,
  `TestWasmRuntimeClose_TimesOutLoudlyOnWedgedDrainer`) — each verified
  (by the reviewer, independently, see below) to actually fail if the
  corresponding fix is reverted, not just pass incidentally.

## Independent review (Opus, isolated worktree)

Ran the full gate itself, walked the WaitGroup accounting by hand across
every `monitorProcess` return path (stopped / restart-disabled /
restart-limit / restart `cmd.Start()` failure / respawn), confirmed the
respawn's `Add(1)` happens-before the respawning goroutine's own `Done()`
so the counter never touches zero mid-restart, confirmed the lock-release-
before-wait ordering in `Shutdown`, and independently verified (not
trusted from the diff's own comment) that in production code
`SubscribeWithHandler`/`Subscribe` on the shared bus have exactly one
caller (`WasmRuntime.Sync`) — every other hit is a test — so `Close`
calling `ResetSubscribers()` can't orphan anything else. Did a real
revert-then-restore TDD check on both fixes (see below).

**Verdict: safe to merge with the one fix the reviewer applied** (pulled
into this branch — see below).

### Finding 1 — HIGH, fixed

`TestSupervisor_Shutdown_AfterCrashRestartCycleDrainsCleanly` failed
deterministically in the reviewer's run:
`query audit_log: SQL logic error: no such table: audit_log (1)`. Root
cause is test infrastructure, not the product fix: `setupTestDB` opens
`sql.Open("sqlite", ":memory:")` with no connection-pool limit, and with
that DSN **every pooled connection gets its own private empty database**.
This new test is the first user of that shared helper to touch the DB
handle from two goroutines at once (the crash/restart monitor's audit
writes racing the test's own polling query), so `database/sql` opened a
second connection into a fresh, tableless DB. The reviewer proved this
with a throwaway concurrent-query probe (8 concurrent queries → 2 open
connections → 6 "no such table" errors), then fixed it by scoping
`db.SetMaxOpenConns(1)` to the two new tests that drive real concurrent DB
access (not the shared `setupTestDB` helper itself, which dozens of
single-goroutine tests use and could deadlock if pinned globally while
holding a `Tx`). Pulled into this branch verbatim, plus one more line: a
stale `audit_events` → `audit_log` comment typo in `server.go` the diff
had re-flowed but not introduced, fixed while in the area.

**Recommended follow-up, not filed as a card** (too small/speculative to
be actionable on its own): `setupTestDB`'s `:memory:` handle is a latent
trap for any future test that touches the DB from more than one
goroutine — worth a comment on the helper itself next time someone is in
that file.

### Finding 2 — MEDIUM, deferred as a follow-up card

`monitorProcess` holds `s.mu` through its entire restart path, including
`time.Sleep(backoff)` (up to `BackoffMax` = 30s under
`AutoStartPlugins`'s policy). `Shutdown` acquires `s.mu.Lock()` at the
very start of its own cancel loop — so if a plugin is mid-restart-backoff
when `Shutdown` is called, `Shutdown` can block up to ~30s on that lock
**before it ever reaches the ctx-bounded `wg.Wait()`** this fix added,
meaning the outer `shutdownCtx`/`backgroundDrainTimeout` budgets can
elapse and `database.Close()` can still run concurrently with a live
monitor — the same race class #380 targets, still reachable in this
specific window.

Verified not a regression introduced by this diff: `git show
main:internal/plugins/supervisor.go` (pre-#380) has the identical
`defer s.mu.Unlock()` + `time.Sleep(backoff)`-under-lock shape. Restart-
backoff policy is an explicit non-goal of #380 ("Changing plugin
restart-backoff policy... is out of scope"), so fixing the lock-holding
sleep itself belongs in a follow-up, not folded into this diff. **Filed
as ut-docs#502.**

### Finding 3 — LOW, accepted as documented behavior

The 5s `shutdownCtx` created in `server.go` is a single budget shared
serially by `srv.Shutdown` → `supervisor.Shutdown` →
`pluginManager.Close`. A slow `srv.Shutdown` can exhaust it, so the two
drains would log their loud ERROR and return without actually having
waited the full amount. This is the shared-ctx design this card's own
brief specified ("reusing the SAME shutdownCtx... don't invent a new
timeout constant") — not a bug, just a real characteristic worth having
on record: the loud error can fire even without a genuinely wedged
goroutine, purely from budget exhaustion earlier in the same shutdown
sequence.

### Finding 4 — LOW, accepted (lifecycle misuse, out of scope)

`s.wg.Add(1)` / `w.wg.Add(1)` can in principle race a concurrent
`Wait()` at counter zero (a `sync.WaitGroup` panic) if `StartPlugin`,
`RestartPlugin`, or `Reload` runs concurrently with `Shutdown`/`Close`.
Requires calling those during a live shutdown — `StopPlugin`/
`RestartPlugin` concurrency with `Shutdown` is explicitly out of this
card's scope per its own non-goals ("`StopPlugin`/`RestartPlugin`... are
out of scope, `Shutdown` is the only entry point this card is about").
Accepted; no application code today calls those during shutdown.

## Verified beyond the automated suite

- **Concurrency walk-through, by hand, all return paths.** Every
  `monitorProcess` exit (stopped / restart-disabled / restart-limit-
  reached / restart `cmd.Start()` failure / respawn tail) traced for
  correct `Add`/`Done` pairing; confirmed no double-count, no
  under-count, no zero-window mid-restart.
- **Revert-then-restore TDD, both fixes, by the reviewer in its isolated
  worktree** (never touching this checkout, per the ut-docs#386
  mitigation):
  - `Supervisor.Shutdown` reverted to its pre-fix body (bare `return nil`,
    no wait) → `TestSupervisor_Shutdown_WaitsForMonitorProcessGoroutines`
    and `TestSupervisor_Shutdown_TimesOutLoudlyOnWedgedMonitor` both
    failed with the expected real errors ("Shutdown returned while a
    monitor goroutine was still running" / "before its ctx deadline — it
    never actually waited"); restored, diff empty, tests green again.
    (`TestSupervisor_Shutdown_AfterCrashRestartCycleDrainsCleanly` still
    passed under this revert, correctly — it guards `Add`/`Done`
    accounting, not the wait itself.)
  - `WasmRuntime.Close` reverted to a no-op →
    `TestWasmRuntimeClose_StopsDrainersAndReturnsPromptly` and
    `TestWasmRuntimeClose_TimesOutLoudlyOnWedgedDrainer` both failed
    ("subscribers still present after Close" / "before its ctx deadline");
    restored, diff empty, tests green again.
- **Production-only-caller check for `ResetSubscribers` safety**, not
  taken on the diff's own comment: `SubscribeWithHandler` has exactly one
  non-test caller (`WasmRuntime.Sync`); `Subscribe` has zero production
  callers at all.
- **Nil-safety traced end to end**: `nil *WasmRuntime`, `nil *Manager`,
  `&Manager{}` with a nil `Wasm` field, and `server.Start`'s
  `pluginManager.Close(shutdownCtx)` with a nil manager — all confirmed
  non-panicking, both by direct test (`TestWasmRuntimeClose_NilSafe`) and
  by the three `server_test.go` call sites that already pass `nil`.
- **Wiring**: exactly one non-test call site of `server.Start`
  (`internal/app/app.go`), updated; confirmed `pluginManager.Close` reuses
  the identical `shutdownCtx` `supervisor.Shutdown` uses.
- Full `go build ./...`, `go vet ./...`, full `go test ./... -race`
  (zero failures, no data race reported anywhere), and all 4 CLAUDE.md
  guards (`guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-i18n`) — run by Dev, by the reviewer in
  its isolated worktree, and once more here after pulling the reviewer's
  fix in.
- **The two recurring bug classes this pipeline keeps re-finding**
  (missing `os.MkdirAll` before a file write; a cwd-relative path where
  `paths.Data(...)`/`paths.Plugins()` belongs) — confirmed not applicable
  by reading the full diff line-by-line, not assumed from the change's
  description: no production file-write path is introduced at all; the
  only `os.WriteFile` calls are in new tests, writing directly into an
  existing `t.TempDir()` or through the pre-existing
  `writeFileWithParents` test helper (which already does `MkdirAll`
  correctly).
- No raw SQL added outside `internal/data`/`internal/db` (none touched at
  all); no user-facing strings (the new log lines are operator-facing
  `logging.L().Errorf` diagnostics, not template output); no money
  involved; no ADR needed or contradicted — this is lifecycle plumbing
  extending the existing `drainBackgroundServices` pattern, not a new
  architectural decision (doesn't touch ADR-0001's wasm-runtime model:
  `Close` stops event drainers only, doesn't call `w.rt.Close()`, correct
  for a goroutine-drain fix and moot at real process exit).
- No real client/shop name anywhere in the diff or its tests (fixture
  IDs are `com.test.*`); no secret-shaped literal.

## Deferred / explicitly out of scope

- **ut-docs#502** — the restart-backoff sleep holding `s.mu` through
  `Shutdown`'s lock acquisition (Finding 2 above), pre-existing, not a
  regression from this card, explicitly excluded from #380's own
  non-goals.
- The shared-`shutdownCtx`-budget characteristic (Finding 3) — by design
  per this card's own brief, not a defect.
- `StartPlugin`/`Reload` racing `Shutdown`/`Close` at the WaitGroup
  boundary (Finding 4) — lifecycle misuse, no application code does this
  today, out of this card's explicit scope.

## Safe-to-merge verdict

Yes, after pulling in the reviewer's test-only fix (`db.SetMaxOpenConns(1)`
in the two new concurrent-DB tests) and the trivial comment correction.
No manual/`web/help/` update needed — this is backend shutdown-lifecycle
plumbing with no shop-owner-visible surface (no new/changed route,
template, or UI copy); confirmed explicitly, not skipped.
