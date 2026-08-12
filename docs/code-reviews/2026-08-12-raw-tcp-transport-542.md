# Code review: raw TCP transport for local-hardware wasm plugins

**Card:** universaltill/ut-docs#542 (p1, hardware, security, complexity: hard)
**Branch:** `pipeline/542-raw-tcp-transport`
**Date:** 2026-08-12
**Complexity:** hard — Dev via a Fable subagent (isolated worktree), Review
via an independent Opus subagent (isolated worktree, deliberately not Fable).
One review round found a real blocking finding (B1); fixed in this same diff
and re-verified, not treated as earning an unscoped second full round.

## What shipped

Local-hardware/device wasm plugins (first user: ZVT payment terminals) can
now speak raw TCP to a LAN device. Architect decision: extend the `wasm`
runtime rather than build `runtime:"go"` host↔process IPC (verified
unbuilt — `internal/plugins/supervisor.go`'s `Supervisor` is pure `os/exec`
process lifecycle with zero event-bus wiring) — a TCP socket isn't the "raw
OS access (USB/serial)" ADR-0001 reserves for `runtime:"go"`, so a new
`tcp:<host>:<port>` permission plus four host functions
(`tcp_open`/`tcp_write`/`tcp_read`/`tcp_close`) mirroring the existing
`net:<host>`/`http_request` pattern was the right-sized choice for one
cycle. Recorded as an "Amendment (2026-08-12)" section on ADR-0001 (not a new
ADR file) — the decision itself doesn't change, only clarifies it.

- `internal/plugins/wasm_tcp.go` (new) — `tcpConnRegistry` (mutex-guarded,
  per-plugin sequential handles, max 4 concurrent), the four host functions,
  `pluginHasTCPPermission`.
- `internal/plugins/wasm_hostfns.go` — registers the four exports.
- `internal/plugins/wasm_runtime.go` — `hasTCP` map mirroring `hasNet`,
  timeout widening in `timeoutFor`, `tcpConns.CloseAll(id)` wired into
  `Sync`'s module-drop loop so a disabled/removed plugin never leaks a
  socket.
- `internal/plugins/testdata/tcp_guest/main.go` (new) — wasip1 test fixture
  guest driving roundtrip/openonly/maxhandles/readtimeout scenarios.
- `internal/plugins/wasm_tcp_test.go` (new) — 15 tests (12 from Dev + 3 from
  this review pass: the B1 regression, an IPv6-addressing unit test, and the
  N5 test-isolation fix folded into the shared helper).
- `ut-docs/adr/0001-plugin-runtime-wasm.md`,
  `ut-docs/architecture/wasm-runtime.md`,
  `ut-docs/reference/plugin-host-functions.md` — updated in the same branch.

## Independent review (Opus, isolated worktree)

Read the full diff against the closest precedent (`hostHTTPRequest`,
`hasNet`/`netTimeout`/`pluginHasNetPermission`), ran `go build`, `go vet`,
the full `go test ./internal/plugins/...` (including `-race`), and
personally reverted-then-restored three pieces of the implementation to
confirm the tests fail for the right reason.

### Findings

**B1 — blocking, FIXED in this diff. The event deadline did not actually
bound a blocked `tcp_*` host call.** `net.DialTimeout`/`SetWriteDeadline`/
`SetReadDeadline` all used only the guest-supplied timeout, ignoring the
`ctx` `HandleEvent` derives from `timeoutFor` (10s for a plugin holding any
`net:`/`tcp:` grant) — unlike `hostHTTPRequest`, which *does* thread `ctx`
through `http.NewRequestWithContext`. The reviewer measured a live
**25.03s** block (`read_timeout_ms: 25000` against a silent fixture, 10s
event deadline) via a throwaway probe test, on a `payment.*.authorize`-class
**Blocking** event dispatched synchronously from the cashier/kiosk tender
request — i.e. a wedged or slow-answering terminal could freeze checkout for
up to ~40s (a read started just before the old deadline, running its full
30s clamp), squarely the "never blocking checkout" promise both the card and
the ADR amendment make.

Fixed: `hostTCPOpen` now dials via `(&net.Dialer{}).DialContext(dialCtx, …)`
with `dialCtx` derived from `context.WithTimeout(ctx, clampedTimeout)`, so
the dial gives up at whichever is sooner. `hostTCPWrite`/`hostTCPRead` now
compute their deadline via a new `effectiveDeadline(ctx, timeout)` helper —
the earlier of "now + the clamped per-call timeout" and `ctx`'s own
deadline — before calling `SetWriteDeadline`/`SetReadDeadline`.

Regression test: `TestHostTCPReadDoesNotOutliveEventDeadline` — a silent
fixture, `read_timeout_ms: 25000` against a 10s (`hasTCP`-widened) event
deadline. **Personally reverted `effectiveDeadline`'s two call sites back to
the plain `time.Now().Add(...)` form and re-ran it: failed at 25.02s**
(`wasm_tcp_test.go:...: HandleEvent took 25.025910088s ... want bounded by
the 10s event deadline`), matching the reviewer's own measurement almost to
the millisecond. Restored the fix: passes at ~11.4s (non-race) / ~24.1s
wall-clock under `-race` where the internal post-setup measurement — the
only thing the test actually asserts on — stays inside the 13s threshold
(the extra wall time under `-race` is wasip1-guest compile + fixture/DB
setup overhead, not the bounded call itself; see the "verified beyond
automated tests" section).

Non-blocking findings from the review, triaged:

- **N1 — FIXED.** `fmt.Sprintf("%s:%d", host, port)` breaks on any IPv6
  literal (`"::1:20007"` → `net.Dial`'s "too many colons in address";
  `hostAllowedScheme` already blesses `::1` for the `net:` sibling, so an
  author following that convention would hit this). Replaced with a new
  `tcpAddr(host, port)` helper (`net.JoinHostPort`) used for both the dial
  address and the permission string, leaving IPv4/hostname behaviour
  byte-for-byte unchanged. New unit test `TestTCPAddr` covers IPv4,
  hostname, and three IPv6 forms.
- **N5 — FIXED.** The package-level `tcpConns` registry's per-plugin handle
  counter isn't reset between runs of the *same* test function, so
  `-count=2`/`-shuffle` turned `TestHostTCPExactGrantRoundTrip` and
  `TestHostTCPMaxHandles` red (handle numbering off by however many handles
  the previous run opened). Fixed once, centrally, in `newTCPTestRuntime`
  (`tcpConns.CloseAll(pluginID)` on both ends via `t.Cleanup`) rather than
  per-test. Verified: `go test ./internal/plugins/... -run TCP -count=2`
  green (was red before the fix, confirmed by reproducing the reviewer's
  exact failure locally before applying it).
- **N2 — accepted as a documented limitation, not fixed.** `tcp_write`/
  `tcp_read` authorize via the open handle, not a fresh `CheckPermission`
  per call, so revoking `tcp:*` mid-flight doesn't close an already-open
  socket until the plugin is next disabled (which does fire `CloseAll`).
  This is a real, correctly-identified gap against the module's own stated
  "every capability is revocable live" invariant, but fixing it (a
  per-call permission re-check) is a small, independent, separately
  reviewable change — filed as a follow-up rather than growing this diff.
- **N3 — accepted, documented, filed as a follow-up.** A plugin *version
  update* (not disable/removal) doesn't fire `CloseAll` — `Sync`'s
  module-drop loop only covers `!active[id]`, and an updated plugin stays
  `active`. Bounded at 4 leaked FDs per update, closed on next
  disable/process-exit. Docs corrected to say so explicitly (see below)
  rather than overclaiming "updated" in the force-close list.
- **N4 — accepted, filed as a follow-up.** `CloseAll` resets the per-plugin
  handle counter, so a disable→re-enable cycle restarts handles at 0 —
  low-impact (a plugin can only alias its own past connections) but
  falsifies the "never reused" doc comment. `next` also never guards
  against `int32` wraparound at 2³¹ opens (aliases a `hostErr*` code) —
  academic at a 4-handle cap, noted for the same follow-up card.
- **N6 — accepted, filed as a follow-up (test-coverage gap, not a product
  bug).** No test proves the `tcp:<host>:<portA>` grant is denied for
  `<portB>`/a different host — the exact-match boundary is the security
  core of the permission model and is currently only proven "some grant
  needed at all" (`TestHostTCPOpenDeniedWithoutPermission`). Also untested
  at the host-fn layer: one plugin's handle rejected for another plugin
  (only covered at the bare-registry unit level).

Two recurring bug classes this pipeline watches for: **not applicable,
confirmed.** No `os`/`path/filepath`/`paths` import anywhere in
`wasm_tcp.go`; the diff touches no disk path at all.

## Verified beyond the automated suite (this session)

- **TDD claim (B1) re-verified personally**, not taken on the Opus review's
  word alone: reverted the fix, reproduced the exact 25.02s failure,
  restored it, confirmed green. See above.
- **N1/N5 fixes each have their own new/modified test**, run red-then-green
  personally (`TestTCPAddr` new; `-count=2` regression for N5 confirmed red
  against the pre-fix helper, green after).
- `go build ./...`, `go vet ./...` — clean. `gofmt -l` — clean on every
  changed/new file.
- `go test ./...` (full suite, all repo packages) — all green, no failures.
- `go test ./internal/plugins/... -run TCP -race -timeout 180s` — all 15
  TCP-suite tests pass, race detector clean (the first attempt at the
  default 120s package timeout was killed by `-race`'s instrumentation
  overhead on the deliberately slow timeout tests, not a real hang — reran
  with a longer timeout and it's clean).
- All five guard scripts green: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh` (953 keys, all locales match),
  `guard-help-topics.sh`.
- **Doc accuracy re-checked against the fixed code**, not just the
  as-submitted diff: `reference/plugin-host-functions.md` and
  `architecture/wasm-runtime.md` are updated to state the per-call timeout
  is *also* bounded by the calling event's own deadline (the B1 fix), and
  to say `CloseAll` fires on disable/removal but **not yet** on a version
  update (N3), rather than the as-submitted "disabled, updated or removed"
  claim the review caught as false.
- No manual/help-topic work owed — this is a plugin-author-facing host
  function surface (`reference/plugin-host-functions.md`,
  `architecture/wasm-runtime.md`), not anything a shop owner sees or does;
  no route, page, or operator-facing control was added or changed.
  `guard-help-topics.sh`'s page-route coverage passes unchanged.
- No real client/shop name, no secret-shaped literal: only RFC1918/loopback
  test addresses (`127.0.0.1`, `192.168.1.50` — the documented ZVT example,
  matching the docs) and synthetic plugin IDs.

## Safe-to-merge verdict

**Yes, with B1 fixed in this diff and N1/N5 folded in as cheap wins.** The
design is a faithful, minimal extension of an already-proven pattern
(`net:`/`http_request`), the registry locking is correct and race-clean, and
the 15 tests are genuinely end-to-end (real `net.Listen` fixtures, real
compiled wasip1 guest, real bytes) — none of them false-pass on inspection.
B1 was real and would have shipped a checkout-freezing defect directly
contradicting this card's and the ADR amendment's own stated guarantee; it's
fixed and independently re-verified red-then-green by two different people
(the Opus reviewer, then me). N2/N3/N4/N6 are genuine, correctly-scoped
follow-ups, not swept under the rug — filed on the board rather than grown
into this diff, per the "several honestly-scoped commits beat one commit
claiming more" standing rule.

**Filed as follow-up backlog cards** (see the closing issue comment):
runtime:`"go"` host↔process IPC for direct USB/serial device plugins
(path 2, deliberately not built here); and a bundled small-fixes card for
N2/N3/N4/N6.
