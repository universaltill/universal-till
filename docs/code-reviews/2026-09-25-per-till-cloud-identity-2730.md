# Review — per-till cloud identity: replicas stop checking in as the main till (ut-docs#2730)

- **Author:** Opus 5.5 (Dev). **Reviewer:** Fable 5.1, fresh context, security focus, one round. Card complexity:hard, p1, `security`.
- **Origin:** product owner report — my. showed stale versions per till. Root cause: `PerTillSettingPrefixes` did not cover `marketplace.*`, so the admin bundle copied the main till's device id, registration marker and **store token** onto every replica; each till then heartbeat as the main till's device with the main till's credential.

## What shipped
1. **Identity never crosses the LAN.** `marketplace.device_*`, `marketplace.token*`, `marketplace.enrolled_at` and `marketplace.public_key` are per-till: `DumpAdmin` never sends them and `ApplyAdmin` never applies them (a pre-fix main till still sends them). The join snapshot deletes the same rows on the served copy under `secure_delete(1)`, so the token is not recoverable from the file's bytes; `ApplyReplicaIdentity` deletes them again at the sink and records `marketplace.device_till_id` for the id it mints. `TestPerTillSettingsCoverTillCloudIdentity` keeps the two prefix lists (`internal/data`, `internal/db`) in step. Store-level keys (`store_id`, `merchant_id`, opt-ins) still travel.
2. **Replica heals at boot.** `repairCopiedIdentity` runs only when `sync.primary_url` is set, `sync.till_id` is known and `marketplace.device_till_id` differs from it: mints `till-<uuid>`, clears `device_registered`/`enrolled_at`, writes the marker last. Env-pinned identities are left alone.
3. **Main till vouches.** New `POST /api/sync/cloud-device` (sync-bearer authed, middleware-exempt like `/api/sync/secrets-key`): the main till calls the cloud's `devices/register` with the token only it holds and answers `{store_id, device_id}` — `enroll.Vouch` has no credential field. `till_id` and `device_name` come from the main till's `tills` row, never the body; the main till's own device id is refused; a till that is itself a replica answers 409. The replica loop asks at boot, re-vouches every 6 h, backs off 30 s..30 min on failure, persists only `device_registered`. `RegisterNow` on a replica vouches instead of creating an anonymous store.
4. **Fallback.** A pre-fix main till (404, or 401 from its session middleware) makes a replica that already holds a legacy token copy register its own device id itself. A replica with no token does nothing until the main till upgrades.
5. **Decision kept:** an existing replica keeps a previously copied token — deleting a copy revokes nothing (same credential the main till still uses) and would cut that till off; rotation to per-device tokens is ut-docs#2751/#2716.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | No cap on `/api/sync/cloud-device`. Every accepted request is a cloud call the main till makes with its own token. A replica — or a compromised one, which is exactly the threat the card names — could make the main till hammer `devices/register`; the cloud keeps no per-store device cap (`org.Metadata["devices"]` grows unbounded) and its per-IP limiter (20 rps / burst 40) would then throttle the **main till's own** cloud sync behind the shop's NAT. | **Fixed (TDD):** `pairRateLimiter` keyed by the authenticated `till.ID` (never by IP, so one noisy replica cannot block a sibling, and an unknown bearer is refused before it can spend a budget): 10 per 10 min, 429 `too_many_requests`; the replica treats 429 as a retryable error and backs off. `TestSyncCloudDevice_RateLimitedPerTill` was red first (`call 11: status 200, want 429`). |
| 2 | Low | A replica may vouch any well-formed device id except the main till's — including a sibling's, whose cloud name/version it could overwrite. Binding device id to till needs the cloud to key devices by `till_id`. | **Accepted, tracked:** ut-docs#2752 (cloud merges/flags by `till_id`; `registerDeviceID` already sends it) and #2716. Bounded by finding 1's cap meanwhile. |
| 3 | Low | 401 from the main till is read as "pre-fix main till". A replica whose bearer was **revoked** (removed from the shop) that still holds a legacy token copy keeps self-registering its device under the store every 6 h. | **Accepted:** grants nothing new — the exposure is the copy itself, which pre-dates this card; only rotation (#2751) closes it. Distinguishing the two 401s is not possible against a pre-fix main till. |
| 4 | Low | A replica joined after this fix has **no** cloud credential: `cloudsync` skips (heartbeat and directives already flow through the main till, #2633), but its diagnostics / issue-report uploads cannot reach the cloud, and its status chip reads "not registered". | **Accepted, tracked:** #2751 (per-device tokens), #2683 (relay diagnostics via the main till or keep a direct path), #2753 (chip wording). Selling is never affected. |
| 5 | Nit | `RedactedJoinSnapshot`'s load-bearing comment for ut-docs#636 ("redaction stays limited to one column on one table") became false. | **Fixed:** comment names the fixed settings-prefix set. |
| 6 | Nit | `TestSyncCloudDevice_RejectsUnauthorized` asserts `Contains("token") && Contains("store-token")` — the second implies the first. | Accepted, harmless. |

## Hunted and found clean
- **Token on the wire:** the only new LAN message is `{device_id, version}` → `{store_id, device_id}`. `TestReplicaIgnoresTokenInVouchAnswer` (hostile main till adds `token`/`merchant_token`) confirms nothing credential-like is persisted or made live. Fallback self-registration only ever uses a token the replica already holds.
- **Spoofing:** till id and name come from the bearer's `tills` row (`syncTill`), not the body; body size is capped at 4 KiB; device id is `^[A-Za-z0-9._:-]{1,128}$`; version truncated to 64.
- **Heal false positives:** a main/standalone till has no `sync.primary_url` → never repairs (`TestInitMainTillIdentityUntouched`); env `UT_MARKETPLACE_DEVICE_ID` never replaced; second boot does not re-mint (marker); a join against an older primary that sends no `DeviceID` still ends with a fresh id plus marker via the repair.
- **Vouch loop races:** `cur` is set before either goroutine starts; the fallback path holds `attemptSem` like every other cloud call; `applyVouch` refuses a vouch for a different device id. `-race` clean.
- **Existing fleets:** replica upgrades first → repair, 401 from the old main → self-registers with its legacy copy (distinct device in my.); main upgrades first → stops sending identity, replica keeps the copied id until its own upgrade; both upgraded → vouch. The old cloud rows stay until #2752.
- Repository pattern: the new SQL is in `internal/db` (`DeleteTillCloudIdentity`, `substr` not `LIKE` because `_` is a wildcard); `guard-data-access.sh` passes. No i18n keys, no help topic (docs card #2754).

## Verification
- **Gates (reviewer):** `go vet` clean, `gofmt -l` empty, `scripts/ci/guard-data-access.sh` passes. `go test -race -count=1` on `internal/data` (125 s), `internal/db` (345 s), `internal/enroll`, `internal/auth` and `internal/pages -run 'CloudDevice|Sync'` (92 s, including the new test) — all pass.
- **TDD re-verified by the reviewer** (each fix reverted in place, its test run, the fix restored, all in one step):
  - marketplace prefixes removed from `PerTillSettingPrefixes` → `TestAdminDumpApplyRoundTrip_MarketplaceIdentityNeverSyncs` fails with `marketplace.token leaked into the admin dump` (and every other per-till key); `TestPerTillSettingsCoverTillCloudIdentity` fails.
  - `DeleteTillCloudIdentity` call removed from `RedactedJoinSnapshot` → `TestRedactedJoinSnapshot_StripsTillCloudIdentity` fails with `marketplace.token still in the join snapshot` and `the store token is recoverable from the served file's raw bytes`.
  - boot-time `repairCopiedIdentity` call removed → `TestInitReplicaWithCopiedIdentityMintsOwnDevice` fails with `device_id = "till-7547e9b4-main", want a freshly minted till-*`.
  - reviewer's own limiter removed → `TestSyncCloudDevice_RateLimitedPerTill` fails with `call 11: status 200, want 429`.
  All green again after restore.
- **Not run:** e2e, docs-shots (no UI change).

## Verdict
Safe to merge after the Dev squashes the reviewer's two edits (rate limit + comment) into the fix commit. Follow-ups already carded: #2751, #2752, #2753, #2754; #2683 decides the diagnostics path.
