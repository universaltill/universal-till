# Code review: kiosk input-freeze diagnosability (ut-docs#1329)

**Date:** 2026-08-30
**Card:** ut-docs#1329, split from ut-docs#1228 (kiosk till intermittently
freezes to all input while app internals stay healthy; restarting
`unitill-desktop` clears it — see #1228 for the full incident report).
**Scope:** diagnosability plumbing only — an input-liveness heartbeat plus
an on-demand state snapshot. No self-recovery/auto-restart logic (that's
the sibling card ut-docs#1330).

## What shipped

- `cmd/unitill-desktop/control.go`: new authenticated `POST /input-heartbeat`
  and `GET /diagnostics` on the existing loopback control server (same
  `withAuth` pattern as `handleExitToOS`/`handleApplyMode`). Tracks
  `lastInputAt`, `lastAppliedMode`, `startedAt` (mutex-protected). Logs the
  control server's own listen **address** (never the token) at startup, and
  logs on every `/diagnostics` hit plus on the first heartbeat / first
  heartbeat after a >2min gap.
- `internal/pages/common`: `WindowController` gains `RecordInputHeartbeat()
  error`, implemented on every controller — `HTTPWindowController` forwards
  to `/input-heartbeat`; `ShellPollWindowController` forwards to its
  spawn-mode fallback when one exists; `KioskSystemdWindowController` /
  `AndroidNativeWindowController` / `NoopWindowController` are documented
  no-ops (no desktop-control-channel process exists on those platforms).
- `internal/pages/window_state_api.go`: new `POST /api/window/input-heartbeat`,
  forwards to `WindowCtl`, always 204s. Logs "first heartbeat"/"resumed
  after a gap" **at this layer too**, independent of whether `WindowCtl`
  had anywhere to forward to — the only path that gives kiosk/Android/pure-
  attach-mode installs any diagnostic trail at all. Left out of
  `auth.exempt()` (ordinary signed-in session, no PIN — this isn't
  destructive like exit-to-os/apply-mode).
- `web/public/input-heartbeat.js`: leading-edge-throttled (5s) listener on
  `pointerdown`/`touchstart`/`keydown`, POSTs the heartbeat with a 4s abort
  timeout, swallows all failures. Loaded from `web/ui/layouts/base.html`
  only — the five standalone documents (login/setup/self_order/
  self_order_shop/order_tracking) are a documented gap, not this card's
  scope.

No new user-facing strings — developer/ops-facing only (logs + JSON).

## Independent review

Opus, fresh context (different model from the Sonnet subagent that wrote
the diff), isolated `git worktree` (never shared the orchestrator's own
checkout — see the `reviewer` skill's ut-docs#386 note on why that matters
for a revert-then-restore TDD check). Ran the whole gate itself
(`gofmt`/`vet`/`build`/`go test ./...`, all CI-blocking guards it had time
for, plus `go test -race` on the touched packages — clean, no data race)
and independently re-verified 4 TDD claims by hand: reverted each
production change (never a test file), confirmed the matching new test(s)
failed for the stated reason, restored, confirmed green again. All four
held. (Two new `internal/auth` tests can't fail against this diff —
`middleware.go` is untouched — so they're documented as regression pins
against a future exemption, not new-behaviour proof.)

Findings and disposition:

| # | Severity | Finding | Disposition |
|---|---|---|---|
| 1 | Blocker | `guard-docs-shots.sh` failed — `base.html`/new JS are in the guard's hashed surface | **Fixed** — `make docs-shots` re-run, manifest + screenshots committed |
| 2 | Blocker | `ShellPollWindowController.RecordInputHeartbeat`'s forward-failure log used `Errorf`, which floods `logging.L()`'s capped `recentBuf` (powers the backoffice "recent problems" panel + ADR-0018's cloud digest) — a wedged/unreachable shell logs every ~5s and evicts all 50 slots in ~4 min, exactly the ut-docs#954 regression class, and self-defeating since a wedged shell is the incident this card investigates | **Fixed** — downgraded to `Infof` (same fix applied to the analogous log in `window_state_api.go`) |
| 3 | Should-fix | `current_window_mode` only updates via `POST /apply-mode`, which on Linux is reached only through the desktop-shell spawn-mode fallback — the initial mode (`showWindow`) and live-poll mode changes (`watchShellMode`) never set it, so it reads `""` on the exact topology #1228's incident happened on | **Deferred, documented in code** (`control.go`'s `lastAppliedMode` field comment) — fixing needs a small hook into two platform build-tagged files (`webview_fallback.go`, `shell_poll.go`) this pass didn't want to land under-tested; the gap is visible (empty string), not silently wrong |
| 4 | Should-fix | `GET /diagnostics` was practically unreachable after the fact — neither the control address nor the token were ever logged | **Fixed** — the control server now logs its listen address (never the token) at startup |
| 5 | Should-fix | Kiosk/Android/pure-attach-mode installs record the heartbeat nowhere at all, so the card's "log line" acceptance criterion silently failed to hold on those topologies | **Fixed** — `window_state_api.go`'s handler now logs "first heartbeat"/"resumed after a gap" unconditionally, independent of `WindowCtl` forwarding |
| 6 | Should-fix | The JS `inFlight` latch had no fetch timeout — a wedged server (the exact condition being investigated) would leave it `true` forever, going permanently dark | **Fixed** — dropped `inFlight` (the 5s throttle already caps concurrency), added `AbortSignal.timeout(4000)` |
| 7 | Nit | New `internal/auth` tests can't fail against this diff | Documented above, not cited as TDD evidence |
| 8 | Nit | `TestHTTPWindowController_RecordInputHeartbeat_NonOKStatusReturnsError` actually tested an unreachable address, not a non-OK status | **Fixed** — renamed to `..._UnreachableReturnsError`, added a real non-OK-status test alongside it |
| 9 | Nit | `settings_page_test.go`'s `inputHeartbeatCall` counter was incremented but never asserted | **Fixed** — removed the dead field |
| 11 | Nit | A comment said "four standalone documents"; there are five (`order_tracking.html` omitted from the count, correctly excluded from scope) | **Fixed** — corrected to five with the reasoning for excluding `order_tracking.html` specifically (an anonymous customer's own phone) |
| 12 | Nit (design note) | An input-only heartbeat can't distinguish "frozen" from "idle/shop closed" | Accepted as a known limitation — a parallel timer-driven alive-tick would resolve it; out of scope for a diagnosability-only card |
| 10 | Nit | The `window_state_api.go` forward-failure log branch is unreachable in production (every real `WindowController` returns nil from `RecordInputHeartbeat`) | Left as defensive logging (now also downgraded to `Infof` per finding 2's fix) rather than removed — cheap insurance if a future controller does return an error |

Blockers 1–2 and should-fixes 4–6 plus nits 8–9/11 are fixed in this diff.
Should-fix 3 is deferred with an explicit code comment; a follow-up card
(ut-docs#1331) tracks it.

## Verified beyond automated tests

- `gofmt -l .` empty; `go vet ./...` / `go build ./...` clean.
- `go test ./...` (whole repo) — all packages pass.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job — pass
  locally, including a fresh `guard-docs-shots` after the fix.
- `go test -race` on the touched packages (reviewer's own run) — clean.
- TDD re-verification (reviewer, by hand, 4 cases) — all reverted-fails,
  restored-passes.

## Safe to merge

Yes, once CI is green on the pushed branch.
