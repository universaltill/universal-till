# ut-docs#6957 - macOS main till published through the OS mDNS responder

Reviewer: combined Tester+Reviewer (fast mode, one pass), not the Dev.
Scope: uncommitted diff on origin/main 96df60ee1 (system_advertise.go, advertiser.go hooks, en.json none_found, e2e string, tests).

## What shipped
On macOS the till registers `_unitill-sync._tcp` via `/usr/bin/dns-sd -R` (OS responder) instead of a raw hashicorp/mdns socket; fallback to hashicorp on start failure; tick restarts the publisher if it exits; wizard "none found" text now lists the checks.

## Findings
| # | Severity | Finding | Outcome |
|---|----------|---------|---------|
| 1 | low | Orphan reaper required the old till id in the ps line, so an orphan advertising a previous id (id changed since the crash) was never reaped. | Fixed: `isOurDNSSD` matches `/usr/bin/dns-sd`, ` -R `, `_unitill-sync._tcp` (still excludes pid reuse and other dns-sd uses); `TestIsOurDNSSD` added. |
| 2 | info | 300 ms start sleep happens under `a.mu`; only delays a concurrent Shutdown/tick by 0.3 s. | Accepted. |
| 3 | info | dns-sd registers all host interfaces (VPN/VM addresses), unlike the hashicorp zone limited to `lanIPs()`. Peers may see a non-LAN A record. | Accepted; browse side verifies by pinging. |
| 4 | info | Exited publisher is dropped without Shutdown, leaving a stale pid file; harmless (reaper checks ps command line). | Accepted. |

Checked, no issue: Wait goroutine vs Kill (os.Process is safe after Wait; errors ignored); Shutdown idempotent (Kill error ignored, Remove ignore-missing); pid file under `paths.Data`, `MkdirAll(paths.DataDir())` before write; exec args with no shell (id is a UUID; TXT with `=` or spaces is one argv element, pinned by `TestDNSSDArgs`); `startSystem` runs after the no-LAN-address and iOS native-bridge branches; two tills with different ids use separate data dirs/pid files.

## Verification
gofmt clean; go build ./... ok; go vet ./internal/discovery/... ok (also GOOS=windows/linux/android arm64); golangci-lint 0 issues; go test discovery, pages, app, config all ok; guard-i18n and guard-i18n-meta pass. Without the production change the discovery test package does not compile (`a.startSystem undefined`), so the new tests fail. Existing advertiser tests set `startSystem = nil`; dns-sd pid set identical before and after the run (only the live app's own process present).
Not done: real phone/e2e run (Dev/orchestrator scope); no visual surface changed beyond one string.

Pre-flight: walked.

Verdict: approve.

Pre-flight: walked reviewer/PREFLIGHT.md; build, vet, lint, guards, tests green (combined Tester+Reviewer, fast mode).
