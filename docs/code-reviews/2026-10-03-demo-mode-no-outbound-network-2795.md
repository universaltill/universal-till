# Review: Till demo mode (UT_DEMO) part 2 — no outbound network (ut-docs#2795)

**Branch:** `feat/2795-demo-no-outbound-network` · **Design:** ADR-0113 §1.6/§1.7
**Dev model:** Opus 5.5 · **Review model:** Fable (independent, two rounds: Tester + Reviewer)

## What shipped

- New package `internal/netaccess`: a process-wide, atomic (`atomic.Bool`)
  demo switch (`SetDemo`/`Demo`); `NewClient`/`NewClientWithTransport`
  (HTTP clients that deny-all once demo is on, otherwise byte-for-byte
  equivalent to the raw client they replace); `DialContext` (same for raw
  dials); `StartService` (gate for background-service starts — skips and
  logs in demo mode).
- `internal/app/app.go` / `internal/pages/init.go`: 16 background network
  services (cloud sync/enrolment, marketplace/revocation/telemetry,
  self-update, alerts, mDNS advertising, LAN sync push/pull, cloud link,
  the auto-update/plugin-update schedulers, order-status stream bridge,
  etc.) now go through `netaccess.StartService` and never start when
  `cfg.Demo` is true. `cfg.Demo` is read once into the switch at the top
  of `Run`, cleared via `defer` when `Run` returns.
- All request-time outbound clients (barcode lookup, AI, issue-report
  upload, voucher/tables/held-sale sync proxies, table-claim proxy, plugin
  OAuth token client, plus every other raw `&http.Client{`/`net.Dial` site
  found in `internal/` — 46 files total) now construct through
  `internal/netaccess`, preserving each site's original timeout/transport/
  keepalive/redirect-policy values.
- `internal/plugins/wasm_hostfns.go` (`hostHTTPRequest`) and `wasm_tcp.go`
  (`hostTCPOpen`): check `netaccess.Demo()` first, deny with
  `hostErrDenied` ("permission denied") before any other check.
- New CI guard `scripts/ci/guard-netaccess.sh` (+ its own `_test.sh`, 15
  regression cases) forbidding raw `http.Client{`/`net.Dial*` outside
  `internal/netaccess`, with narrow exemptions (the package itself,
  `_test.go` files, full-line comments, and a reviewed
  `// netaccess:allow <reason>` marker — used exactly twice today, both
  for genuinely local-only probes that send no packet off-host:
  `internal/lanip/lanip.go`'s UDP-connect route lookup and
  `internal/server/server.go`'s loopback probe of the till's own
  listener). Wired into `.github/workflows/ci.yml` right after the
  existing `guard-data-access` pair.

## Verified beyond automated tests

- Independent **live-binary run** (Tester): booted the real server both
  demo-off (normal boot, `/healthz` 200) and demo-on (real migrated DB +
  `demo_instance` flag + marker file + token), pointed the marketplace
  endpoint at a local TCP listener, and confirmed **zero connections**
  reached it over 12s of demo-mode operation. Logs showed the expected
  "not starting network service `<name>`" line for all 16 gated services.
- Two independent rounds of **TDD mutation testing** (revert the fix →
  confirm the new test fails with the exact expected error → restore →
  confirm green), across five different sites in total: Tester did the
  mDNS `StartService` gate in `app.go` and the demo check in
  `hostHTTPRequest`; Reviewer separately did `internal/updates`'
  `checkOnce`, `internal/selfupdate`'s `download()`, and the (test-less,
  guard-only) plugin OAuth token client — all five reverts produced a real
  failure (a test failure for the first four, a `guard-netaccess.sh`
  exit 1 naming the exact file:line for the fifth), and all five restores
  came back clean.
- `go test -race ./...` on every package that touches the global switch or
  runs background goroutines while it's set (`netaccess`, `app`,
  `selfupdate`, `updates`, `cloudsync`, `plugins/marketplace`,
  `fleetlink`, `discovery`): **zero `DATA RACE`**. The switch is
  `atomic.Bool`, not a bare bool.
- Full gate, run independently by both Tester and Reviewer from a clean
  tree: `go build ./...`, `go vet ./...`, `gofmt -l internal/` (clean),
  `go test -count=1 ./...` (79 packages ok, 0 FAIL/panic lines — checked
  the full log, not just the tail), `guard-netaccess.sh`,
  `guard-netaccess_test.sh`, `guard-data-access.sh` (pre-existing guard
  unaffected).
- Behaviour-preservation spot checks across 8 distinct call sites (4 by
  Tester, 4 different ones by Reviewer): every site's prior timeout, TLS
  config, keepalive and redirect policy confirmed unchanged with demo off.
- Plugin signing (ADR-0006): `TestManifestSignatureStillVerifiedInDemoMode`
  confirmed to be a real regression guard (genuine fixture passes, a
  tampered copy and a wrong key are rejected, all with the demo switch
  on) — there was never a demo-conditional bypass in the verification
  path, so this pins that staying true rather than proving a live risk.
- Offline-first (ADR-0003): confirmed no inverted gating condition; a
  dedicated test (`TestRun_DemoOffStartsEveryNetworkService`) pins that a
  real (non-demo) till still starts the exact same 16 services as before.
- No UI surface touched (confirmed via diff — no `.html`/`web/`/template
  files), so the visual-review and help-topic requirements don't apply.
  No real client/shop name or literal secret in any new test/seed data.

## Findings

| # | Severity | Status | Summary |
|---|---|---|---|
| F1 | Medium | **Not fixed — filed as a Backlog follow-up** | `internal/netreach` (the status-bar cloud-reachability probe) is reachable in demo mode: `GET /ui/net-status` is on the demo allow-list, polled every 10s by every visitor's browser, and its `Monitor.Status()` fires a real `http.DefaultClient` GET at the configured marketplace-endpoint host's `/healthz`. `http.DefaultClient` isn't `http.Client{`, so the new guard can't see it. Not a merge blocker (the hosting-layer egress deny in §3 absorbs it, and this is pre-existing code this card never touched), but the card's "no outbound network" guarantee is incomplete until it's closed. Recommended fix (reviewer's suggestion, not applied here): disable the monitor outright in demo mode (zero requests, light reads "Unknown" as it did pre-#3095) rather than just routing it through `netaccess` (which would still make one request before failing fast to "No internet") — do both for defence in depth. Needs filing before the demo goes live. |
| F2 | Low | Informational, noted | Skipping `enroll.Init` in demo also skips the local device-identity bootstrap (`device_id` minting). No visible effect today (every surface that would show it is demo-denied or cloud-gated), but the eventual demo-seed card (ADR-0113 §1.11) should know the seed needs to carry any identity value it wants displayed. |
| F3 | Info | No action needed | Guard coverage audited by hand for every other outbound primitive used anywhere in `internal/`/`cmd/`/`main.go` (`http.Get/Post/Head`, `net.DialTimeout`, `tls.Dial`, `websocket.Dial`, `grpc.Dial`, DNS lookups, `ListenUDP`). Every one resolves to a site already behind `netaccess` or a demo-denied route, or is one of the two legitimate `netaccess:allow` local-only probes — except F1. |

## Verdict

**Safe to merge.** Both independent passes (Tester, then a second
independent Fable review in its own worktree) re-ran the full gate from
scratch rather than trusting the prior pass's say-so, each did its own
live TDD mutation checks on different sites, and both reached the same
conclusion: the design (one atomic switch read at request time, one
client/dial construction seam, a mechanical CI guard with its own
regression test) is sound, "no behaviour change with demo off" holds on
inspection and under `-race`, and the one real gap found (F1, pre-existing
`netreach` code this card didn't touch) is not a regression introduced by
this change and doesn't block it.
