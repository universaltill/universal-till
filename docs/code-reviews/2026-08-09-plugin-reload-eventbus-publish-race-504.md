# Code review: EventBus.publish vs. ResetSubscribers race on plugin install/uninstall (Manager.Reload)

**Card:** universaltill/ut-docs#504
**Date:** 2026-08-09
**Complexity:** hard — Dev via a Fable subagent (self-contained brief,
uncommitted diff left in the shared working tree), Review via an
independent Opus subagent (isolated from the Dev's own reasoning),
deliberately not Fable. **Two review rounds**: the first found a
blocker-class (security/DoS) issue, earning a second, scoped round per
this pipeline's process-depth rule — the second re-reviewed only the fix
for that finding, not the whole diff again.

## Background

`ut-docs#503` fixed a reproducible `panic: send on closed channel` where
`Manager.Close`'s `EventBus.ResetSubscribers()` could race a live
`EventBus.publish()` at shutdown, by sequencing `pluginManager.Close`
strictly after every background publisher drained (`internal/app/app.go`).
That fix covered `Manager.Close` only. The identical hazard is reachable
at **runtime**, not just shutdown: `Manager.Reload` → `WasmRuntime.Sync`
calls the same `ResetSubscribers()` on every plugin install/uninstall —
both the cloudsync-ticker-driven cloud-directive path
(`cloudInstallPlugin`/`cloudRemovePlugin`) and the interactive
plugin-store handlers — with no equivalent drain discipline, and none is
possible: installs happen while the till is running, with live publishers
(a checkout's `sale.completed`, `cloudsync`'s ticker) that can never be
"stopped first". #503's own independent review found this gap and filed
it as #504.

## What shipped

**Root-cause fix, not another call-site patch** (per the issue's own
explicit ask): `EventBus.publish()` (`internal/plugins/ipc.go`) now holds
`eb.mu.RLock()` across its ENTIRE dispatch loop — subscriber snapshot
through every channel send / blocking-handler call — instead of releasing
it right after the snapshot. `ResetSubscribers()` needs the exclusive
`Lock()`, so Go's `sync.RWMutex` makes the two mutually exclusive: no
subscriber channel a live `publish()` might still touch can be closed
until that call fully finishes. This closes the race for **both**
`Manager.Close` (shutdown) and `Manager.Reload` (install/uninstall) at
the same root, making #503's `app.go` sequencing defense-in-depth rather
than the only protection.

**Reentrancy handled explicitly** (the real implementation risk in this
class of fix): several helpers `publish()` used to call self-`RLock`
internally — `eb.dbHandle()`, `eb.GetEventMode()`, and (transitively)
`eb.auditEvent()`/`eb.auditDispatch()`. Once `publish()` itself holds the
lock for its whole body, calling any of these is a recursive `RLock`,
which Go's own docs warn can deadlock once a writer is pending. Fixed by:
a locally-captured `db := eb.db` field read (no nested lock call), an
inlined `eb.eventModes[eventType]` read replicating `GetEventMode`'s
default, and two new unexported `...WithDB` audit variants
(`auditEventWithDB`/`auditDispatchWithDB`) that take `db` explicitly —
with the existing `auditEvent`/`auditDispatch` reduced to one-line
wrappers so every other caller (`Ask`, `AskPlugin`, `Acknowledge`) is
unaffected.

**New test**: `internal/plugins/publish_reload_race_test.go` —
`TestPublish_NeverPanicsRacingManagerReload` races a live, never-stopped
publisher goroutine against 100 real `Manager.Reload` calls (unlike
#503's own test, which explicitly drains the publisher before each
`Close()` — proving that discipline is no longer required for
correctness).

### Round 2 — a blocker-class finding, fixed in the same PR

The first Opus review round found the fix correct and safe on its own
terms (see "Independent review, round 1" below) but surfaced a real,
newly-introduced amplification: holding `eb.mu.RLock()` across a
**Blocking**-mode handler call (payment authorization, `.ask`, `.refund`
hooks — `WasmRuntime.HandleEvent` running a wazero module) is only
actually bounded if the timeout `HandleEvent` computes is *enforced*. It
wasn't: wazero's `WithCloseOnContextDone` is off by default and this repo
never enabled it, so `context.WithTimeout`'s deadline was silent — a
CPU-bound guest (buggy or malicious `plugin.wasm`) ran forever regardless.
Pre-#504, that only hung the one publishing goroutine; post-#504's
lock-extension, it would permanently wedge the **entire bus** — including
`HasSubscribers`/`Generation` on the checkout tax-rate-ask path
(`internal/pages/tax_hook.go`, called per basket line) and every future
`Manager.Reload`/`Close` — since Go's `RWMutex` blocks new readers once a
writer is pending. Security-labeled, checkout-path blast radius: earned a
second round per this pipeline's rule.

Fixed, two complementary changes:

1. **`internal/plugins/wasm_runtime.go`**: `NewWasmRuntime` now builds the
   wazero runtime with `WithCloseOnContextDone(true)` — makes the timeout
   `HandleEvent` already computes and applies actually terminate a wedged
   guest, per wazero's own documented guidance for untrusted code.
2. **`internal/plugins/ipc.go`**: `publish()` releases `eb.mu` specifically
   around the `Blocking`-handler call (re-acquired immediately after,
   before any return path, so the function's single deferred
   `eb.mu.RUnlock()` stays correct). Safe because `mode` is fixed for the
   whole `publish()` call (computed once from `eventType`, not
   per-subscriber) — so every subscriber this loop visits when this branch
   runs is Blocking too, and Blocking dispatch never touches
   `sub.Channel`, the sole source of the original panic. A concurrent
   `ResetSubscribers`/`Subscribe`/`SetEventMode`/`BumpGeneration` racing
   this specific call therefore can't touch anything the call still needs.

New test: `TestPublish_BlockingHandlerDoesNotWedgeBus` — blocks a
`Blocking` handler mid-call on a channel, then asserts a concurrent
`ResetSubscribers()` still completes within 2s. Verified to have teeth:
temporarily reverted just the lock-release-around-the-handler change and
confirmed the test fails (`ResetSubscribers blocked >2s behind a live
Blocking handler`) before restoring the fix and confirming it passes.

## Independent review, round 1 (Opus, isolated worktree)

Re-derived the lock-scope/return-path shape from source rather than
trusting the Dev's report: confirmed exactly one `RUnlock` in `publish`
(the `defer`), all six return paths audited and covered. Hunted for
reentrant `eb.mu` calls across every function reachable from the critical
section — `auditEventWithDB`/`auditDispatchWithDB`, `CheckPermission`, the
production `sub.Handler` path (`WasmRuntime.handle` → `HandleEvent` → `w.mu`
+ wazero + host functions) — confirmed none touch `eb.mu`, and separately
confirmed no `w.mu`→`eb.mu` lock-order inversion (every `w.mu` critical
section in `wasm_runtime.go` checked; `Sync` releases `w.mu` before calling
`bus.ResetSubscribers()`).

**TDD verified independently, not trusted**: ran the new test against a
scratchpad copy with `ipc.go` reverted to `HEAD` (working tree never
mutated) — panics 3/3, exact `send on closed channel` at the original
channel-send line; with the fix, passes; repeated `-count=5` under `-race`
with no flake or hang; #503's own test still passes.

**Full gate**: `go build`, `go vet`, `gofmt -l` clean (only the 4
pre-existing dirty files, unchanged); all 5 CLAUDE.md guards pass;
`go test ./... -race -count=1` green across 35 packages with one
exception — `TestSupervisor_Shutdown_TimesOutLoudlyOnWedgedMonitor`,
chased to ground as a **pre-existing flaky test unrelated to this diff**
(reproduces on unfixed `HEAD` under load; root cause is the test itself
anchoring its deadline check before `time.Now()`, `shutdown_drain_test.go`
— filed separately, not this card's concern, not touched here).

Diff hygiene confirmed clean (`ipc.go` + new test file only, all
untouched functions byte-identical to `HEAD`) before the round-1 verdict:
**safe to merge**, with the Blocking-mode lock-duration finding tracked
as a should-fix given the card's p2/security label and checkout-path
reach — see round 2 above for its resolution.

## Verified beyond the automated suite

- Real production call path throughout: `SharedBus`, real
  `Subscribe`/`SubscribeWithHandler`/`Publish`, real `Manager.Reload` — no
  mocks.
- Lock-order inversion explicitly ruled out (not just assumed): every
  `w.mu` critical section in `wasm_runtime.go` read and confirmed it never
  calls back into `eb.mu`.
- The round-2 finding's fix independently probed empirically before
  landing: a minimal `loop br 0 end` wazero module confirmed
  `WithCloseOnContextDone`'s absence meant a 1s timeout did not terminate
  a CPU-bound guest after 10s; a never-returning `Blocking` handler
  confirmed it wedged `ResetSubscribers`/`HasSubscribers`/`Publish`/
  `Generation` for >3s pre-fix.
- `TestPublish_BlockingHandlerDoesNotWedgeBus` proven to have teeth by
  reverting just its own fix and watching it fail, not merely written and
  assumed correct.
- Full `go build ./...`, `go vet ./...`, `gofmt -l .` (clean on every
  touched file), full `go test ./... -race -count=1` (green, modulo the
  pre-existing unrelated flake noted above), and all 5 CLAUDE.md guards.
- No user-facing strings, no `web/help/` update needed (Go-only
  concurrency fix in `internal/plugins/`). No ADR — a bounded concurrency
  fix to an existing mechanism, not new architecture; this review record
  is the design note the issue asked for.

## Deferred / explicitly out of scope

- The N+1 audit `InsertAuditRaw` calls per publish now run under the
  RLock (previously lock-free) — bounded by SQLite's `busy_timeout(5000)`,
  accepted as a minor, documented tradeoff, not a defect.
- `Ask`/`AskPlugin` unchanged — they never touch `sub.Channel`, no race
  there.
- `TestSupervisor_Shutdown_TimesOutLoudlyOnWedgedMonitor`'s pre-existing
  flakiness — real, but unrelated to this diff; worth its own card.

## Safe-to-merge verdict

Yes. Independently reproduced and independently re-verified fixed —
twice, the second round earned by a real blocker-class finding — by a
different model than the one that authored each fix, with all gates
green. No real client/shop name anywhere; no secret-shaped literal
introduced.
