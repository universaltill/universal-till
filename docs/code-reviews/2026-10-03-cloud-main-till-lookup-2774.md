# Review: cloud-assisted main-till lookup, till side (ut-docs#2774)

- Date: 2026-10-03
- Branch: `feat/2774-cloud-main-till-lookup`
- Author: Opus 5.5 (lane:cloud-41b). Tester: session default. Reviewer: Fable (independent,
  different model from both).
- Companion: ut-cloud `feat/2774-main-till-address` (the endpoint this talks to).

## What shipped
- `internal/discovery/cloud_lookup.go` (new): `NewCloudLookup(creds)` GETs
  `<marketplace endpoint>/v1/stores/main-till-address?store_id=…` with this till's **own** device
  bearer (ut-docs#2730 identity), 5 s client timeout, redirects refused, 4 KiB body cap. A
  `404`/`405` is "old cloud": no candidate, no error, not asked again for an hour (same shape as
  `cloudsync/checkin.go`'s `checkinRetryOld`). Any other non-200 is an error for an info log and is
  retried at the next re-discovery. `cloudCandidateURL` accepts only a literal IPv4:port (no
  hostname, never a DNS hop the proof's host binding does not cover).
- `internal/discovery/primary_watch.go`: `rediscover()` gains a second candidate source, asked
  **only when the mDNS browse yielded zero usable candidates** (including "only other shops'
  tills" and "browse errored"). The cloud's answer is appended to the same `ordered` list and goes
  through the same `challenge()` / `primary-proof` loop; a failed proof is a `WarnProblemf` under
  the existing `MainTillProblemKey` and `continue`, never a switch. `SetCloudLookup` is the wiring
  seam (nil = mDNS only, exactly as before). `MinBrowseInterval` / `UnreachableThreshold` gating is
  untouched, so the cloud is asked at most once per browse interval.
- `internal/cloudsync/cloudsync.go`: `LANAddressKey = "lan_address"`; `buildSyncRequest` strips
  it from the device report whenever `sync.primary_url` is set (`isReplica`), **not** on the
  reported `role` string — a `display.mode == "backoffice"` till with no `primary_url` is the real
  main till and must still report (the Tester's finding, now pinned by test).
- `internal/pages/cloud_main_till_address.go` (new): `mainTillLANAddress` = `lanip.IPv4()` +
  the port from `cfg.ListenAddr` (the same source `app.go`'s mDNS advertiser publishes);
  `primaryWatchCloudCredentials` = `enroll.Effective(cfg).Marketplace` endpoint / store id /
  device token. `cloudsync_wire.go` contributes `lan_address` through the existing `DeviceExtra`
  hook inside the existing `SyncPrimaryURL == ""` block; `init.go` wires `SetCloudLookup` before
  `StartSyncPull`.
- Help: `web/help/{en,de,ar,fa,tr}/multitill.md` step 2 of "When a joined till can't reach the
  main till" now says the joined till also asks the shop's cloud account, that the same proof
  applies, and that both tills must be connected to the cloud for this to work.
- Tests: `discovery/cloud_lookup_test.go` (contract, old-cloud backoff, transient errors retried,
  unenrolled makes no call, redirects not followed), `discovery/primary_watch_cloud_test.go` (nine
  cases incl. failing proof never switches, relayed proof never switches, cloud not asked when the
  LAN has a candidate, rate-limited with the browse, non-IP answers never dialled),
  `cloudsync/lan_address_test.go` (four role/`primary_url` combinations),
  `pages/cloud_lan_address_test.go`, and `pages/sync_primary_watch_cloud_test.go` (in-process:
  real lookup client over HTTP, real `/api/sync/primary-proof` handler, impostor refused, real
  main till accepted, bearer unchanged).

## Independent checks (Fable)
- **Proof gate is the only switch path.** The cloud candidate is a plain `Candidate` in the same
  slice; the only `settings.Set("sync.primary_url", …)` in `rediscover` is after a successful
  `challenge()`. The bearer is never sent to a candidate (`sawBearer` asserted in both the
  failing-proof and relay tests).
- **Offline-first (ADR-0003).** `rediscover` is reached only through `ContactFailed`, whose
  callers are the sync pull tick (`sync_admin.go`) and the link client's dial failure
  (`sync_link_client.go`) — background goroutines, never a sale-path handler. The lookup adds at
  most 5 s to one pull tick per `MinBrowseInterval`, and only after a browse that already spent
  `browseTimeout` finding nothing. No new goroutine, no new cadence.
- **Fallback only.** `len(ordered) == 0` is the one entry point; in strict mode other-shop tills
  are never in `ordered`, so a shopping-centre LAN still falls through to the cloud (tested).
- **Role conflation.** The gate is `sync.primary_url == ""`, the same rule as
  `discovery.RoleCheckFromSettings` and the `cloudsync_wire.go` block it sits in. No plain
  `== "primary"` string comparison was added anywhere in `internal/cloudsync`, `internal/discovery`
  or `internal/pages` (grep, non-test).
- **Port correctness.** `mainTillLANAddress` and `app.go`'s `listenPort(cfg.ListenAddr)` read the
  same value, so the cloud-reported port is the one the mDNS TXT record advertises.
- **Recurring bugs:** no file write anywhere in the diff (no `os.MkdirAll`/`paths.Data` concern);
  no cwd-relative path.
- **Locale keys:** `web/locales/*.json` untouched (`git status` clean there). The new
  `WarnProblemf` text follows #2722's exact precedent (English problems-digest line under the same
  key), so no new UI string. Help prose is present and consistent in all five locales and is not
  stale: it describes the new behaviour in one sentence inside the existing re-discovery step.
- **No real client/shop names; no secret literals** (`tok-1`, `replica-device-token`,
  `device-token-1` are obvious placeholders; RFC 5737 / RFC 1918 addresses only).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | info | `cloudCandidateURL` accepts loopback and link-local answers that the cloud side never stores. Asymmetric on purpose (tests listen on loopback; the proof makes a wrong address cost one refused challenge). | Accepted; documented in the function comment. |
| 2 | info | A non-strict (pre-#2722, no proven main-till id) replica that sees *any* other till on mDNS challenges those and does not ask the cloud. Consistent with "fallback only"; such replicas gain the proven id on their first successful re-link. | Accepted. |
| 3 | nit | `TestPrimaryWatch_CloudLookupIsRateLimitedWithTheBrowse` relies on the fixture's `now` seam; fine, but the browse-interval invariant it pins is the only thing stopping a stranded replica from hammering the cloud. The cloud's own per-device limiter (burst 6, 1/min) is the backstop. | Accepted. |
| 4 | minor (gate red) | **The full gate was not green as handed over.** `internal/data`'s `TestSettingScope_EveryUsedKeyIsClassified` scans every `…Key` string constant as a settings key and failed on `cloudsync.LANAddressKey` (`lan_address`), which is a cloud device-report field, not a setting. Confirmed branch-introduced: fails on the pre-review snapshot; `origin/main` has no such constant. Dev/Tester's "full `go test ./...` green" claim did not hold for this package. | **Fixed** by the reviewer: `cloudsync.LANAddressKey` added to the scanner's `notSettingsKeyConsts` exclusion list with its reason, the same way `pos.ServiceChargeKey` and the i18n message keys are excluded. `go test ./internal/data/` passes; nothing outside that test file changed. |

| 5 | minor (CI red) | **`guard-docs-shots.sh` failed on the PR's first head** (`build` job): the help-topic edit changed the `multitill` markdown hashes for `en/fa/ar/tr`, and the `internal/pages/*.go` edits changed the surface hash, but `web/help/img/manifest.json` was not refreshed. `de` is not flagged because the manifest tracks only `en, fa, ar, tr` (`e2e/tests-docs/lib.js` `LOCALES`), so nothing is left inconsistent there. The `multitill` screenshot is `/tills` (the Tills page, lib.js:76), not the help text, and this PR touches no `web/ui` template, so no pixel can change — but the guard's remedy for topic-hash drift is `make docs-shots`, and the surface-only escape hatch (`update-docs-shots-surface-hash.sh`) says in its header it is not a substitute when a topic's markdown changed. | **Fixed** by the reviewer with a real `make docs-shots` run: 120/120 captures passed, **every PNG came back byte-identical** (so the committed set was produced on this same container image; the pre-installed Chromium 141 vs. the 149 pin — the Playwright CDN is denied by the egress proxy — produced no drift), and the only change is the five-line `manifest.json` refresh (surface hash + four `multitill` topic hashes). `guard-docs-shots.sh` passes locally. Committed separately with a `Docs-Shots-Unchanged: true` trailer, the repo's convention for a no-pixel refresh. |

Nothing blocking after findings 4 and 5; no production code changed in review.

## Verification
- **TDD, re-verified by the reviewer** in a detached `git worktree` off the `WIP: pre-review
  snapshot` commit (never the shared checkout), mutate → run → `git checkout --` restore → re-run:
  - `primary_watch.go` mutated so a cloud answer sets `sync.primary_url` without a challenge:
    `TestPrimaryWatch_CloudCandidateFailingProofNeverSwitches` fails ("relinked to a cloud-supplied
    address that failed the proof") and `TestPrimaryWatch_CloudCandidateRelayNeverSwitches` fails
    ("relinked to a relay the cloud pointed at"). Restored: `ok`.
  - `cloudsync.go` gate changed to `role != "primary"` (the Dev's original bug):
    `TestBuildSyncRequestLANAddressOnlyFromPrimary/backoffice-mode_main_till_(no_primary_url)`
    fails ("lan_address present = false, want true"). Gate removed entirely: the `replica` and
    `backoffice-mode_replica` subtests fail ("present = true, want false"). Restored: `ok`.
- **Gate:** `gofmt -l .` empty, `go build ./...`, `golangci-lint run ./...` 0 issues,
  `go test -count=1 ./...` — one failure as handed over (finding 4, `internal/data`), fixed in
  review and that package re-run green; every other package was green on the same production tree
  (the fix is confined to a `_test.go` file in `internal/data`). `go test -race` on
  `internal/discovery`, `internal/cloudsync` and the new `internal/pages` tests: `ok`. The PR's CI
  is the hosted full run on the final tree.
- **Not run:** two physical tills on a client-isolated Wi-Fi. The in-process test drives the real
  lookup client, the real proof handler and the real pull tick; what it cannot prove is that a
  given router's isolation behaves as assumed, which is the whole reason the feature exists.

## Deferred
- None in this repo.

## Verdict
**Safe to merge.** Part of ut-docs#2774; the card is Done only once ut-cloud's companion PR is
also merged and DevOps has confirmed the deploy.
