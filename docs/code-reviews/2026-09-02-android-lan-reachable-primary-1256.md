# 2026-09-02 — Android till can be a LAN-reachable primary (ut-docs#1256)

- **Repo/area**: `mobile/mobile.go` (embedded server bind), `mobile/mobile_test.go`,
  `android/README.md`, `android/app/src/main/res/xml/network_security_config.xml`
- **Card**: universaltill/ut-docs#1256, `complexity:hard`
- **Dev model**: Fable (subagent) · **Review model**: Opus (subagent, isolated worktree)

## What shipped

Android's embedded till server was hardcoded to bind `127.0.0.1` only, so
it could never be discovered/paired-with as a shop's primary till — only
usable as a satellite/replica. Per the product owner's 2026-09-02 decision
on the issue (find on the LAN, join over a specific token, not usable by
an unpaired device), this change extends ADR-0033's **already-shipped**
discovery + approve-to-pair + per-till bearer-token model to Android,
rather than designing a new security mechanism:

- `mobile/mobile.go`'s `Start` now binds the server to `0.0.0.0:<port>`
  (`UT_LISTEN_ADDR`) while still returning `127.0.0.1:<port>` to the
  native shell — the in-process WebView contract is unchanged; a wildcard
  bind still answers on loopback.
- `freePort()` now probes `0.0.0.0:0` (not `127.0.0.1:0`) so the probed
  port is guaranteed free on the interface the server actually binds —
  see Finding 1 below for why the mismatch mattered.
- `network_security_config.xml`/`android/README.md` comments corrected —
  no functional change there: Android's Network Security Config governs
  only the app's own outbound Java/WebView-layer cleartext, never a
  `gomobile`-embedded Go server's raw sockets (inbound or outbound).
- `ut-docs/adr/0023-android-ios-till-strategy.md` gets a 2026-09-02
  addendum recording the decision, why no new ADR was needed (ADR-0033
  already covers it), and two explicitly-open items (below).

TDD: `TestStart_ListensOnAllInterfacesButReturnsLoopbackAddress` added
first; independently re-verified failing against the pre-fix code
(`mobile_test.go:217: UT_LISTEN_ADDR host = "127.0.0.1", want the
all-interfaces 0.0.0.0 bind`) and passing after, both by the implementer
and again by the independent reviewer in an isolated worktree.

## Independent review findings

Reviewed by a fresh Opus subagent in an isolated git worktree (not the
shared checkout — see `reviewer` skill's ut-docs#386 note), with its own
adversarial security audit of every `/api/sync/*` and `/api/*` route
against `internal/auth/middleware.go`'s exemptions.

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | `freePort()` probed `127.0.0.1:0` while the real bind moved to `0.0.0.0` — not the harmless microsecond TOCTOU the old comment described, but a **reliable failure mode**: a port free on loopback but held on a specific LAN interface would pass the probe, fail the real bind, and `internal/server.listenWithFallback` (ut-docs#1169) silently degrades a failed wildcard bind back to `127.0.0.1` on a *different* port — which `waitUntilReady` isn't polling, so `Start` would hang the full 30s and fail. | **Blocking** (real correctness bug) | **Fixed** — `freePort()` now probes `0.0.0.0:0`, eliminating the mismatch. |
| 2 | Doc/comment claims that the new bind matches what "desktop/Linux/Pi" already ships were wrong for desktop specifically: `cmd/unitill-desktop/desktop.go` binds `127.0.0.1` only (its own separate WebView-shell design) — only the bare Linux/Pi **service** binary (`UT_LISTEN_ADDR` default `:8080`) is wildcard-bound. | Non-blocking (factual accuracy) | **Fixed** — corrected in `mobile/mobile.go`'s package/`Start` comments, `android/README.md`, and the ADR addendum; "desktop" replaced with "Linux/Pi service till" throughout, `cmd/unitill-desktop` called out explicitly as unaffected. |
| 3 | The ADR addendum's description of the original mDNS phantom-`127.0.0.1`-entry bug attributed it to the wrong mechanism (implied the TXT record carried an address; it doesn't — see `advertiser.go`'s `txtRecord`). The real path is `internal/discovery.localIPs()` (a `net.InterfaceAddrs()` walk, independent of the HTTP server's own bind address) feeding the advertised SRV/A record that `browse.go`'s `candidateFromEntry` turns into `base_url`. | Non-blocking (doc accuracy) | **Fixed** — addendum corrected; also now states explicitly that this fix does not, by itself, prove `localIPs()` sees Android's Wi-Fi interface (see open items below). |
| 4 | No `CHANGE_WIFI_MULTICAST_STATE` permission / `WifiManager.MulticastLock` anywhere under `android/` — many Android Wi-Fi drivers drop inbound multicast (mDNS uses `224.0.0.251:5353`) an app hasn't explicitly asked to receive. This fix makes Android *reachable* (direct IP, manual/QR pairing, an already-paired replica's sync) but doesn't, on its own, prove Android is *discoverable* by another till's browse scan. | Non-blocking for this diff (real functional gap, not a regression this diff introduces) | **Not fixed here — filed as universaltill/ut-docs#1469**, `blocked:env` (needs a real device, not actionable from a cold cloud session). ADR addendum states this explicitly as open, not resolved. |
| 5 | `internal/recovery/serve.go`'s boot-failure recovery page (`GET /` renders raw error detail, `POST /api/recovery/retry`) sits entirely outside `auth.Middleware`/ADR-0033 and is now LAN-reachable on Android same as it already is on the Linux/Pi service till. Correctly gated by `requireLoopback`? No — it is NOT loopback-gated (only the *sales-journal* routes inside recovery mode are). This is pre-existing behavior on every wildcard-bound till today, not a regression this diff introduces, but it sits outside the ADR-0033 boundary this change cites as its security justification. | Informational | **Accepted, not fixed here** — genuinely pre-existing and identical across every platform that already binds wildcard; out of scope for a card about Android specifically. Noted here so it isn't lost; worth a dedicated look if the recovery page ever carries more than boot-failure diagnostics. |

Full audit result (finding negative, i.e. no gap found): every one of the
~250 routes under `internal/pages/**`, including all 11 `/api/sync/*`
data routes, is gated by either the bearer token (`syncTill`,
`sync_api.go:166`), a manager/session check, rate-limiting +
possession-of-secret (the pairing endpoints), or is intentionally
anonymous by an existing, separate design decision (`/self-order/*`,
ADR-0020). The `0.0.0.0` bind exposes no endpoint class beyond what a
default-configured Linux/Pi service till already exposes today.

## Verified beyond automated tests

- `gofmt -l`, `go build ./...`, `go vet ./...`: clean.
- `go test ./mobile/... -v -race`: all pass, including on the LAN-reachability
  leg (this sandbox has a real non-loopback IPv4; the test's HTTP GET
  against it returned 200, not just the loopback leg).
- `go test ./internal/discovery/... ./internal/app/... ./internal/server/...
  ./internal/auth/... ./internal/pages/... -race`: zero regressions.
- Full-repo gate (`go test ./... -race`) run before commit: one
  package-level false alarm, ruled out before merging — `internal/pages`
  hit the default 600s per-package `go test` timeout inside a single
  unrelated test (`TestSyncPullTick_BrokenPluginRefetchBacksOffAfterRepeatedFailures`,
  plugin-sync backoff logic, nowhere near this diff). That test passes in
  13s run in isolation with `-race`; this repo's actual CI (`.github/workflows/ci.yml`)
  never runs the full suite with `-race` at all and already runs
  `internal/pages`/`internal/plugins` as separate steps with their own
  timeouts specifically because of this class of slowness (see the
  workflow's own ut-docs#643/#753/#776 comments) — so this was a resource-
  contention artifact of one big local `-race ./...` invocation, not a
  regression from this diff. Confirmed against the real gate: all three
  PR checks (`ci`, `UI E2E`, `commit-attribution`) passed on GitHub
  Actions.
- Guards: `guard-android-status-address.sh`, `guard-android-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-i18n.sh`, `guard-data-access.sh`,
  `guard-help-topics.sh` — all pass.
- No real client/shop name, no literal secret anywhere in the diff
  (grepped, confirmed clean).
- UI/help-topic check: N/A, confirmed rather than assumed — this diff is
  backend/config-only, no user-facing screen or behavior a shop owner
  would see changed; `guard-help-topics.sh` passes.

## Deferred / explicitly out of scope

- universaltill/ut-docs#1469 — real-device verification of mDNS
  multicast reception and `localIPs()`'s interface enumeration on
  Android (Finding 4 above).
- The fiskaly satellite/main-till billing enforcement (separate card per
  the product owner, not a security/transport concern).
- Any opt-in UI toggle for "LAN-exposed primary mode" — the approve-to-pair
  flow itself is the consent gate; none was added or is needed.

## Verdict

**Safe to merge.** One real (Finding 1) and two accuracy (Findings 2, 3)
issues found and fixed; one real functional gap (Finding 4) correctly
scoped out and tracked as a follow-up rather than blocking this fix,
since this change is a necessary prerequisite for it either way; one
informational, pre-existing, non-regressing item (Finding 5) recorded
for visibility.
