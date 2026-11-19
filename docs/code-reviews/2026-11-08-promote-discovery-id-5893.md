# Review — promote mints a fresh LAN discovery id (ut-docs#5893)

**Date:** 2026-11-08 · **Branch:** `fix/5893-promote-discovery-id`
**Author model:** Sonnet (dev subagent) · **Reviewer:** Opus 5.5 (independent subagent) · **Tester:** Opus 5.5 (separate subagent)

Pre-flight: Till user-visible behaviour → `web/help/en/multitill.md` updated; docs-shots n/a (prose only, no screen changed); `reference/` n/a (no page documents the discovery-id lifecycle — ADR-0033 does); README n/a. New/changed `en.json` key n/a (none). Amended ADR → ADR-0033 "two till ids" amendment extended in ut-docs (`docs/5893-promote-discovery-id`, record `code-reviews/2026-11-08-promote-discovery-id-5893.md`); no superseding ADR needed. Follow-ups → #5896, #5897.

## What shipped
- `SettingsRepo.ClearReplicaIdentity` (the promote transaction) now writes a fresh `uuid.NewString()` to `lan_discovery.till_id`, unconditionally, before the `sync.*` delete. On a joined till that shop-wide key is its former main till's id (synced down by admin sync), so a promoted till kept advertising it over mDNS, computing verification codes and ADR-0114 primary proofs with it, labelling its sales-copy rows with it, and filtering a till with that id as "self" in scans/join watch.
- `data.LANDiscoveryTillIDSettingsKey` is the one definition; `discovery.TillIDSettingKey` points at it. Discovery package comments now say the id changes on promote.
- Help: the promote paragraph in `multitill.md` says the promoted till gets its own new Till ID and other tills join it again.

## Tests (TDD, red → green re-verified by Reviewer and Tester with only the settings_repo.go fix reverted)
- `TestSettingsRepo_ClearReplicaIdentityMintsOwnDiscoveryID` (data): old id replaced; missing id minted; cloud id still copied (regression guard, passes either way).
- `TestSyncPromote_PromotedTillAdvertisesOwnDiscoveryID` (pages): after POST promote, `discovery.TillID` is a new UUID, `ReportedTillID` unchanged.
- `TestDiscoverPrimariesAPI_PromotedTillSeesOldMainTill` (pages): the old main till is listed by the scan after promote, not filtered as self.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | Minor | An admin pull already in flight when promote commits (`syncPullTick` reads `sync.primary_url`, pulls, then `applyAdmin` without re-checking replica state) can upsert the old main's `lan_discovery.till_id` back — the same pre-existing race as `till.name` (#3030). | Deferred → ut-docs#5896 (fix belongs in `applyAdmin`, affects more keys than this card) |
| N1 | Nit | Discovery package comments still said the id is stable per install. | Fixed |
| N2 | Nit | ADR sentence said tills learn the new id "through pairing"; code-based joins learn it from the scan/join snapshot and keep it as proven `sync.primary_till_id`. | Fixed in the ADR |
| N3 | Nit | The cloud-id subtest is a regression guard, not failing-first. | Accepted, noted above |
| T1 | Info (Tester) | No test forces the new write to fail to prove it shares the transaction; code reading confirms it is inside the tx before `DELETE`/`Commit` with early return. | Accepted |

Checked and fine: nothing caches the id (advertiser `startLocked` reads it fresh; pairing codes, pending pairings, proof server, fleet hello, scan/join-watch filters and `salesMirrorSource` read it per use); cloud `ReportedTillID` prefers `till_identity.cloud_id`; promote-time adoption reads the old main's id from mirror state, not the setting; steady-state admin sync cannot overwrite it (only tills with `sync.primary_url` pull). No file writes or paths added.

## Extra verification (Tester, real binary)
One till seeded as joined (old discovery id, cloud id, admin PIN): not advertising before promote; after `POST /api/sync/promote` `lan_discovery.till_id` was a fresh UUID, `sync.*` cleared, cloud id kept, a real mDNS browse found it advertising the new id at the next 30 s tick, and its own scan hid itself by the new id. Not driven: a second till pairing with the promoted one; no e2e spec covers promote.

## Gates
`gofmt -l .` empty; `go build ./...`; `go vet` (touched packages); `golangci-lint run ./...` 0 issues; `go test` data/discovery/pages pass (root `internal/pages` needs >10 min under parallel load — passes with `-timeout 40m`); `-race` targeted promote/discovery/pairing pass; guards data-access, i18n, help-topics, fixed-dates, core-neutral, sales-mirror-isolation, help-drift, help-terms, help-breadcrumbs, mail-addresses, compliance-claims pass.

## Verdict
Approve.
