# Kiosk input-liveness heartbeat + on-demand snapshot (ut-docs#1329)

**Card:** ut-docs#1329 — split from #1228 (Pi5-1 input-freeze incident,
2026-08-28). **Complexity:** medium at pick-up; **re-labelled hard** by this
review — see verdict below. **Dev:** Sonnet (inline, `lane:cloud-54`).
**Review:** Opus, two rounds (fresh-context each, no shared history with
dev or with each other beyond this record).

## What this card asked for

Plumbing so the *next* Pi5-1-style freeze yields a root cause instead of
another anecdote: a kiosk-side JS heartbeat on real user input, forwarded
to `unitill-desktop`'s control channel, plus an on-demand snapshot
(last-input age, window mode, process uptime, control address) — no
self-recovery action (that's the sibling card, #1330).

## What shipped

- `web/public/kiosk-heartbeat.js` — throttled (15s) `pointerdown`/
  `touchstart`/`keydown` listener, fire-and-forget POST to
  `/api/kiosk/input-heartbeat`. Loaded unconditionally in
  `web/ui/layouts/base.html`, `login.html`, `setup.html` (see "Round 1"
  below for why NOT gated on `{{ kiosk }}`).
- `internal/pages/kiosk_heartbeat_api.go` — new auth-exempt handler,
  server-side 2s throttle (see "Round 1" finding 2), relays to
  `Deps.WindowCtl.InputHeartbeat()`.
- `WindowController` interface gained `InputHeartbeat() error`; all five
  implementers updated (`NoopWindowController`, `HTTPWindowController`,
  `KioskSystemdWindowController`, `AndroidNativeWindowController`,
  `ShellPollWindowController` — the last delegates to its spawn-mode
  `fallback`, unconditionally, unlike `ApplyMode`).
- `cmd/unitill-desktop/control.go` — new `POST /input-heartbeat` and
  `GET /snapshot` on the shell's existing loopback-token control server;
  new `SetMode` method wired into `webview_fallback.go`'s launch-time
  apply and its ADR-0064 poll callback (see "Round 1" finding 3).
- Real regression tests throughout (auth exemption + narrowness, throttle,
  `-1`/`""` sentinels, fallback forwarding, an actual HTTP round trip per
  new endpoint). TDD claim independently re-verified via an isolated git
  worktree: reverted every production file this card touches (kept the
  tests), confirmed every touched package fails to **compile** with the
  exact `undefined: InputHeartbeat` / `undefined: registerKioskHeartbeat` /
  `undefined: snapshot` errors expected, restored, confirmed green again.

## Independent review — round 1 (Opus, fresh context)

Verdict: two blocker-class findings, both fixed same-round.

**B1 — the feature shipped structurally inert.** The heartbeat `<script>`
was gated on `{{ if kiosk }}`, which tracks `UT_KIOSK=1` — the Pi
headless-**cage** appliance (`KioskSystemdWindowController`, no
`unitill-desktop` process, no control server to relay to at all).
`unitill-desktop` — the only host of `/snapshot`, and what Pi5-1 actually
ran — never sets `UT_KIOSK`, so the script never rendered there. The two
halves of the feature were unreachable together on Linux. **Fix:** removed
the `{{ kiosk }}` gate; the script now loads unconditionally (same pattern
`autofill.js` already uses in the same three files) — harmless where
there's no live channel, since `Deps.WindowCtl` already no-ops safely.

**B2 (security-adjacent) — unauthenticated, unthrottled, LAN-reachable
relay.** `/api/kiosk/input-heartbeat` is auth-exempt by design (must work
pre-login) and, unlike the shell's own loopback-only control server, is
reachable from anywhere on the shop LAN. Unthrottled, a hostile or
misbehaving LAN client could fire unbounded concurrent POSTs, each fanning
out to an up-to-3s outbound call against the shell's control listener — a
resource-exhaustion shape against the same channel `exit-to-os` depends
on; the 15s client-side throttle is trivially bypassed by anything that
isn't that exact script. **Fix:** server-side floor,
`kioskHeartbeatMinInterval = 2s`, closure-scoped mutex + timestamp; a
throttled POST still returns 204 (never surfaced as an error) but skips
the relay.

**N1 (data quality) — `window_mode` silently uninformative.** `cs.mode`
was only ever written by `handleApplyMode` (the HTTP `/apply-mode` path),
but the ADR-0064 default (attach mode, and the launch-time initial apply
in *every* mode) calls `applyWindowMode(w, mode)` directly, in-process,
never touching that handler — so the field would read `""` for the entire
life of the steady-state common case. **Fix:** new `controlServer.SetMode`
wired into both direct call sites in `webview_fallback.go`, guarded the
same way `SetOps` already is.

## Independent review — round 2, scoped to the three fixes (Opus, fresh context)

Mechanical re-verification (gofmt/build/vet/full test suite/`-race` on the
new and touched tests/all relevant guards) — clean throughout both rounds.

**B2's throttle and N1's `SetMode` verdict: correct, no issues.** Read
`webview_fallback.go`'s two new call sites specifically (this file needs
GTK/WebKit cgo libs this sandbox doesn't have, so it can only be checked
by careful reading, not compiled here) — `ctl` is a read-only function
parameter never reassigned in `showWindow`, both sites are correctly
scoped and nil-guarded, no lock held across `w.Dispatch`, no new
fabrication (`applyWindowMode` is `void` everywhere, so recording "this is
what we told it to do" is exactly as honest as the pre-existing
`handleApplyMode` path). The 2s floor is a reasonable choice: bounds
fan-out to at most 2 concurrent 3s outbound calls, and since the target
state (`lastInputAt`) is process-global rather than per-client, a
process-wide floor is the coherent choice — the worst case is a few
seconds of granularity loss on a signal read in minutes-long freeze
incidents.

**B1's fix closes the *template* half but exposed a deeper gap: the
feature is still inert on the real target topology.** On a `.deb`/Pi
install, `unitill-pos` runs as its own systemd service and
`unitill-desktop` takes the ADR-0064 **attach** branch —
`ShellPollWindowController` with a **nil** `fallback` (no env handoff at
all, by design: attach mode exists specifically to *not* need one). So
`InputHeartbeat()` on that path returns having relayed nothing, `/snapshot`
reports the `-1` sentinel permanently, and — worse — nobody can even
*reach* `/snapshot` in that topology: the control-server address and token
are exported only into a spawned child's environment, and attach mode
spawns no child. **The end-to-end path is live and correct only for a
spawn-mode shell** (per `ShellPollWindowController`'s own doc comment,
kept for "a spawn-mode shell too old to poll" — i.e. not the topology a
fresh install runs today).

**Verdict on B1's residual gap: documented, not fixed in this change.**
Closing it needs the heartbeat signal threaded through the *existing*
bidirectional long-poll (`GET /api/window-mode?control=live` already
carries shell→server traffic every few seconds) rather than a new
discovery/secret-sharing mechanism between the two processes — real
surface in `internal/pages/window_state_api.go` and the shell's own poll
client (`shell_poll.go`/`webview_fallback.go`), genuinely beyond what
"medium" scoped for and beyond what a same-cycle scoped re-review round
should absorb. **Re-labelling `complexity:hard`** and leaving this
documented as a known gap (see the "KNOWN GAP" comments in
`internal/pages/kiosk_heartbeat_api.go` and
`cmd/unitill-desktop/control.go`) rather than silently claiming the card's
acceptance criteria are met for the topology the incident actually
happened on — the same "never fabricate success" standard ADR-0064 itself
established for this exact `WindowController` abstraction.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` / `go vet ./...` | clean / clean |
| `go test ./internal/pages/... ./internal/pages/common/... ./internal/auth/... ./cmd/unitill-desktop/...` | all `ok` |
| `-race`, scoped to every new/touched test | clean, no reports |
| `guard-osk-loaded.sh` / `guard-htmx-loaded.sh` / `guard-autofill-suppression.sh` / `guard-i18n.sh` / `guard-help-topics.sh` / `guard-data-access.sh` / `guard-kiosk-engine.sh` / `guard-compliance-claims.sh` | all pass |
| Live end-to-end (built the binary, ran with/without `UT_KIOSK=1`) | script now loads unconditionally either way; `/api/kiosk/input-heartbeat` returns 204 pre-login; a rapid second POST is throttled (still 204) |
| TDD claim | independently re-verified via an isolated git worktree — red (compile failure, exact expected errors) without the production files, green with them restored |

## What this delivers vs. what's still open

**Delivers now, safely:** a spawn-mode shell (the ADR-0064-documented
legacy path) gets working diagnosability end-to-end; every other topology
(the current default included) is a proven-safe no-op, not a silent lie;
the new auth-exempt route is throttled against the LAN-reachable abuse
shape review found; full regression coverage; zero UI surface, zero
user-facing strings, offline-first (fire-and-forget, `try`/`catch`, no
retry/queue).

**Still open (`complexity:hard`, follow-up needed):** attach-mode
diagnosability — the actual gap Pi5-1 hit — needs the input-liveness
signal routed through the existing long-poll channel instead of the
spawn-mode-only push channel. Recommended shape is in both "KNOWN GAP"
code comments and this record; worth an Architect pass (touches the
long-poll response contract both processes already share) before a future
cycle builds it.
