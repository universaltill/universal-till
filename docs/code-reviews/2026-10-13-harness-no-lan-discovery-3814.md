# Review — harness tills never do LAN discovery (ut-docs#3814)

Date: 2026-10-13 · Branch `fix/3814-harness-no-lan-discovery` · Lane `lane:cloud-41`
Built by Sonnet (card `complexity:easy`), reviewed by an independent Opus 5.5
subagent in a separate worktree.

## What shipped

- `UT_LAN_DISCOVERY` (`internal/config.Config.LANDiscovery`): default on; an
  unparseable value counts as on, so a typo never silences a real till.
- `internal/discovery` process-wide switch (`SetLANDisabled`/`LANDisabled`),
  set once by `app.Run` next to `netaccess.SetDemo` and cleared on return.
  When off: `scan` (Browse, BrowsePrinters, the native bridge) returns
  `nil, nil` without a socket; `Advertiser.Start` logs once and starts
  nothing; `DiscoverPrinters` skips the :9100 subnet sweep and its probes.
- Every throwaway till launcher sets `UT_LAN_DISCOVERY=0`: all
  `e2e/run-till*.sh`, `e2e/tests/worker-till.ts`, the `Makefile` `e2e`
  target and the CI e2e job's app start.
- New guard `scripts/ci/guard-e2e-no-lan-discovery.sh` (+ `_test.sh`), wired
  into the `build` job: any e2e launcher (or the root Makefile) that sets
  `UT_LISTEN_ADDR` must set `UT_LAN_DISCOVERY=0|false`; comment-only mentions
  don't count.
- README: "LAN discovery (developers and testing)" section.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | major | `DiscoverPrinters`' :9100 subnet sweep + IPP/ESC-POS probes still ran with discovery off, contradicting the README/config comment; a real LAN printer could reach a harness till's UI. | **Fixed**: gate at the top of `DiscoverPrinters`; `TestDiscoverPrinters_LANDisabledNeverSweeps` (fails without the gate: "subnet sweep enumerated hosts 1 times"). |
| F2 | minor | The scan test installed a native bridge, so the raw-socket path was never exercised. | **Fixed**: `TestScan_LANDisabledNoBridgeNeverQueriesMDNS` (fails with the scan gate neutered: "mdnsQuery called 1 times"). |
| F3 | minor | No Go test that `UT_LAN_DISCOVERY=0` reaches `SetLANDisabled` in `app.Run`. | **Accepted**: two-line wiring next to `SetDemo`; covered by the driven run below (the log line appears only through that wiring) and by the guard on the launchers. A Run-level test needs disproportionate harness. |
| F4 | minor | Guard false-passed on a comment-only mention and didn't scan the Makefile. | **Fixed**: comment lines stripped; default run also scans the root `Makefile`; two new guard test cases; a Makefile regression was simulated and caught. |
| F5 | nit | CI e2e job's app start lacked the var. | **Fixed**. |
| F6 | nit | With discovery off, "Find a primary" reports none found. | Accepted — harness only. |

## Verified beyond unit tests

- Driven run: `bash e2e/run-till.sh` booted; log shows
  `LAN discovery off (UT_LAN_DISCOVERY): not advertising over mDNS`, no mDNS
  client lines after 35 s, `/ui/join-notice` empty. Control run with
  `UT_LAN_DISCOVERY=1`: the log shows `mdns: Closing client …` (the JoinWatch
  browse ran). Servers killed afterwards.
- TDD re-verified independently by the reviewer (both original tests fail
  with the gates removed) and by the orchestrator for F1/F2.
- `gofmt`, `go build ./...`, `go vet`, `golangci-lint` (0 issues),
  `go test -race` on discovery/config, every `run: bash scripts/ci/*.sh` step
  of `ci.yml` locally. Three guards failed locally for tooling reasons only
  (local shellcheck 0.11 vs pinned 0.9; deadcode binary built with go1.26),
  left to CI. Full `go test ./...`: all packages ok except `internal/plugins`
  hitting Go's 10 min default timeout under concurrent load (untouched by
  this diff) and a one-off `internal/pages` locale read race
  (`ar.json: unexpected end of JSON input`) that passes in isolation; CI is
  the arbiter. Reviewer saw `TestRun_JoinsBackgroundGoroutinesOnEarlyServerError`
  fail under load on `origin/main` too — pre-existing, not this change.

## Verdict

Safe to merge after F1/F2/F4/F5 fixes (done). No user-facing strings, SQL,
file writes or paths touched.

## Follow-up after CI (playwright)

`tests/tills-lan-discovery.spec.ts` failed on the PR: with discovery off the
scan answers instantly, so `#discover-msg` never showed the transient
"Searching…" the spec asserted. The spec now holds
`/api/sync/discover-primaries` with `page.route` until "Searching…" has been
seen, then lets the real handler answer; its header comment no longer claims
a real mDNS broadcast. Passed 3/3 locally (`--repeat-each=3`). The run's five
"flaky" specs (settings two-pane, phone popups, money comma, locations
dialog, backoffice scroll) passed on retry and touch nothing in this diff.
