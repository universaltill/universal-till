# Code review — entitlement.* is per-till; link nudges only on a real admin change (ut-docs#2792)

- **Date:** 2026-09-25
- **Card:** universaltill/ut-docs#2792 (p1, bug, complexity:easy)
- **Author:** pipeline `:24` cloud lane (Opus 5.5)
- **Reviewer:** independent Fable subagent, two rounds (round 2 scoped to the round-1 fix)

## What shipped

Found on a Pi (the #2783 investigation): the main till rewrites the four
`entitlement.*` settings on every cloud sync tick (`cloudsync.cacheEntitlement`
sets a fresh `last_confirmed_at`). They were in the admin bundle, so the main
till's admin fingerprint moved every tick and every replica did a full admin
re-pull + plugin convergence check every 1.5–2 min. The ADR-0114 link also
nudged every linked till whenever `sync_admin_version` moved — and the
`settings` trigger bumps it for *every* settings write, per-till keys included.

1. `internal/data/sync_admin_repo.go` — `"entitlement."` joins
   `PerTillSettingPrefixes`: never dumped, never applied (same pattern as the
   #2730 marketplace identity and the #2783 theme).
2. `internal/pages/sync_link.go` — `runLinkAdminWatch` confirms a moved
   generation against `AdminFingerprint` and nudges only when the bundle a
   replica would pull actually changed. Any per-till write (entitlement,
   printer, sync cursors, auto-update bookkeeping) no longer nudges. On a moved
   generation this is the same one scan the replica's pull would cost, and it
   primes the shared generation-keyed cache for that pull. A fingerprint error
   logs at most once a minute and retries next tick.
3. Relay for token-less replicas (round-1 finding): `internal/entitlement`
   gains `Cached` / `ReadCached` / `Cached.RelayValues`; the main till's
   `POST /api/sync/cloud-device` vouch answer carries its confirmed cache;
   `enroll.applyRelayedEntitlement` stores it **verbatim** — the main's
   `last_confirmed_at` included, so the 7-day grace stays anchored to the main
   till's last real confirmation. Skipped when the replica holds a store token
   (its own cloud sync is fresher), when the block is invalid, or when it is
   older than the confirmation already held.

Docs: `ut-docs` `architecture/lan-sync.md` (per-till list + nudge gating) and
an amendment note on ADR-0060 §4.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1-1 | major | Post-#2730 replicas have no store token, so never run `cacheEntitlement`; with entitlement per-till they'd drift to `local` (contrary to ADR-0060 §3/§4), while comments claimed "every till syncs itself". | **Fixed** — vouch-answer relay (item 3), comments + lan-sync corrected, ADR-0060 §4 note. |
| R1-2 | minor | Silent `continue` on fingerprint error in the watch. | **Fixed** — rate-limited warn. |
| R1-3 | nit | Change landing between a new link's hello and the watch's first baseline tick is only caught by the replica's own poll. | Pre-existing, same as before; accepted. |
| R1-4 | nit | The no-nudge pages test drains 300 ms; a very slow box could miss a spurious nudge (false negative only). | Accepted. |
| R2-1 | low | `cur.Token` missed an env-configured token. | **Fixed** — `currentStoreAuth(m)` (effective token) passed into `applyVouch`. |
| R2-2 | low | An older relayed confirmation could overwrite a newer one (out-of-order vouch answers, re-pointed replica). | **Fixed** — monotonicity check, tested. |
| R2-3 | low | Four separate `kv.Set`s: a partial write could briefly pair a new plan with an old status. `last_confirmed_at` is written last, which caps it. | Accepted — enroll's `Settings` has no `SetMany`; bounded to one vouch interval, and the only reader today is `GET /api/entitlement`. |
| R2-4 | low | Loop-based negative test could pass vacuously. | **Fixed** — `TestApplyRelayedEntitlementKeepsCache` drives the guards directly, with a positive control. |
| R2-5 | info | Mixed versions: a new replica paired to a pre-#2792 main gets no entitlement (old main sends it via admin sync → dropped; no relay) and degrades to Local after 7 days. | Accepted — upgrade the main till first (main tills already lead the fleet's version, #2732). Nothing gates on the plan yet. |
| R2-6 | info | Relayed values use the main's clock; `EffectivePlan`'s symmetric ±Grace bound covers skew. | Intentional, documented on `Cached`. |

Security (round 2): the relay decodes under the existing 16 KiB limit, enum-
checks plan/status, re-emits both timestamps as RFC3339, and writes only the
four fixed keys. No credential crosses the LAN (the pages test still asserts no
`token` anywhere in the answer). A hostile main could set a replica's plan, but
it already controls every synced admin setting, and entitlement never gates a
sale (ADR-0060 §5; `TestSalePathNeverImportsEntitlement` still passes with
enroll's new import).

## TDD verified

Each new test was run red against the pre-fix code with the real error, then
green: the data tests (`entitlement.plan is admin-synced`, cursor moved
`…→…`, leaked into the dump), `TestSyncLink_PerTillSettingWriteDoesNotNudge`
(a second `sync admin` frame arrived), `TestSyncCloudDevice_RelaysMainTillsEntitlement`
(relayed entitlement = nil), `TestReplicaWithoutTokenStoresMainTillsEntitlement`
(timed out). The reviewer re-verified the data, link and replica tests
independently in a separate worktree and confirmed the `sync_link.go` change is
needed on its own (the data fix alone still nudges).

## Gate

`gofmt -l .` empty, `go build ./...`, full `go test ./...` green,
`golangci-lint run ./...` 0 issues, every `ci.yml` build-job guard green
except three that also fail on `origin/main` in this container
(`guard-deadcode-baseline*` — no GTK/WebKit headers so `cmd/unitill-desktop`
is skipped; `guard-shellcheck-version` — no shellcheck binary). Real CI covers
them.

Not verified: a driven two-till run on hardware (the Pi5-1 fleet from the
card). The link/replica tests use a real httptest server with a real WebSocket
and a real linked replica, which is as close as this cloud lane gets. No UI
surface changed, so no screenshots.

## Verdict

Safe to merge.
