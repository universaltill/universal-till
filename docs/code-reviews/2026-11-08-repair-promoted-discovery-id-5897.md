# Repair the discovery id of tills promoted before #5893 (ut-docs#5897, Part A)

Part A only: Part B (tills that already have joined siblings) needs an ADR-0114 section 12 amendment first and is a separate card.

- `SettingsRepo.RepairInheritedDiscoveryID` (`internal/data/settings_repo.go`): one transaction. Skips a replica (`sync.primary_url`) and a till with no stored id. Detects a promoted-before-#5893 till by `mirror_state.main_till_id` == own id (passed in by the caller, guard-sales-mirror-isolation) or a non-blank `till_identity.cloud_id` with no own-id marker. Defers, writing nothing, while any `tills` row holds `bearer_hash`/`bearer_prev_hash` (a till that paired against the old id) or a live pending/approved pairing exists. Otherwise pins `till_identity.cloud_id` to the old id when blank (so `ReportedTillID` is stable), mints a new id, writes the marker `till_identity.discovery_id_own`, and audits `discovery_id_repaired`.
- `upsertFreshDiscoveryID` is shared with `ClearReplicaIdentity`, which now also writes the marker (a till promoted on this build is never re-minted).
- Boot: `internal/app/discovery_id_repair_boot.go`, wired after `backfillItemSKUsOnPrimary`, before the advertiser and server. Failure is logged, never fatal.
- No migration, no UI string. The marker sits under the existing per-till `till_identity.` prefix.
- Docs: ADR-0033 "two till ids" amendment gets one sentence (ut-docs branch `docs/5897-discovery-id-repair`).
- Tests: `settings_repo_discovery_repair_test.go`, `discovery_id_repair_boot_test.go`, `TestDiscoverPrimariesAPI_RepairedTillSeesOldMainTill`.
- Restore re-arm (added in review, finding 1): the repair also records the replaced id in `till_identity.discovery_id_inherited` (`TillIdentityInheritedDiscoveryIDSettingsKey`). A stored id equal to it (signal `restored`) re-arms the repair.

## Review (independent, Opus 5.5; code by Sonnet)

Scope: `git diff main..fix/5897-repair-inherited-discovery-id` (WIP `8c503d7f5`), plus the ut-docs ADR-0033 sentence.

### Findings
1. **Medium. Fixed.** A restore of a pre-repair backup brought the old id back for good. `keepLiveCloudIdentity` keeps `till_identity.*` live: the marker and `cloud_id`. It does not keep `lan_discovery.till_id`, which comes from the backup. A restored pre-repair backup therefore put the former main till's id X back with the marker still set. S2 stayed false, and S1 only helps a till whose sales copy is on. The till went back to advertising X permanently: the #5893 bug again, with nothing left to repair it.
   - Fix: the repair records X in `till_identity.discovery_id_inherited`. That key is per till, stripped from the join snapshot, and kept live on restore. When the stored id equals it, the till repairs again under the same gates.
   - Matching on the exact recorded id was chosen over a looser "marker != id" rule. A different till's own id restored over this till (the ut-docs#5173 decision to keep the id) is still left alone.
   - Tests: `TestRepairInheritedDiscoveryID_RestoredPreRepairBackup_RemintsAgain` (red with the `restored` case disabled: `after restore: {Repaired:false …}`) and `TestRepairInheritedDiscoveryID_RestoredOtherOwnID_Untouched`.
   - ADR-0033 sentence extended to match ("and again if a backup restore brings the replaced id back").
2. **Low. Accepted (design decision D2/R5).** The S1-only path pins `till_identity.cloud_id` to X, the former main till's id. A never-joined former main till also reports X as its `ReportedTillID`, so the duplicate till id the cloud already sees stays as it is, rather than becoming a one-time `identity_conflict`. That is not a regression, because the pre-repair state was identical. Noted for the Part B card (#5904).
3. **Info. Accepted.** `salesAggregateSelfTill` and the fleetlink hello read `discovery.TillID` directly.
   - The sales-aggregate fallback applies only when no marketplace device id is set. On such a till the key moves from X (shared with the old main till) to the new id, which is an improvement.
   - The fleetlink hello is only seen by linked tills, and the credential gate means there are none.

### Gate completeness (checked, no finding)
- Credentials: `tills.bearer_hash` / `bearer_prev_hash` are the only stored credentials this till issues. The join snapshot NULLs both, and admin pulls redact both, so inherited roster rows never count.
- QR/short enrolment tokens (`enrolTokens`) live only in memory, so a reboot burns them.
- Approve-to-pair is `pending_pairings` with `pending`/`approved` and an unexpired `expires_at`. It uses the same RFC3339 UTC comparison as `PairingRepo`.
- Replica detection uses trimmed `sync.primary_url`, the same rule as `RoleCheckFromSettings`.
- Order:
  - The step runs after `ApplyPendingRestore`/`ApplyReplicaIdentity`/`CheckRestoredInstall` (app.go:252–278) and before `NewAdvertiser` (:494), `pagesInit` and `server.Start`.
  - No goroutine is started before it.
  - Nothing caches the id: `TillID` reads it each call.
- A till joined again later: the join snapshot strips `till_identity.*`, so the replica is skipped. A later promote writes the marker through `ClearReplicaIdentity`.
- The transaction: every read and write is in one transaction, and the audit goes through `InsertAudit(ctx, tx, …)`. A trigger-injected failure rolls everything back, which is tested.
- Recurring bugs: there is no file write, so `os.MkdirAll` and `paths.Data` do not apply.
- Rules: raw SQL only in `internal/data`, no migration, no user-facing string, test dates from `time.Now()`.

### Extra verification (reviewer)
- TDD claims re-verified by breaking the code, seeing the named test fail, and restoring it:
  - Dropping `OR bearer_prev_hash IS NOT NULL` makes `TillHoldsCredential_Deferred` fail with `Repaired:true`.
  - Removing the marker write from `upsertFreshDiscoveryID` makes `ClearReplicaIdentityWritesOwnDiscoveryIDMarker` fail with `marker "" vs id …`.
- After the fix:
  - `gofmt` is clean. `go build ./...` and `go vet` (data, app) pass.
  - `go test` passes for `internal/data`, `app`, `db`, `discovery`, `enroll` and `pages`.
  - `golangci-lint run ./...` reports 0 issues.
  - These guards pass: data-access, fixed-dates, sales-mirror-isolation, card-data-schema, mail-addresses, kiosk-engine, no-showmodal, rtl-logical-css, page-http-error, netaccess, core-neutral, i18n, compliance-claims.
- Tester (tester.md):
  - Mutation table: 16 mutations, each caught.
  - Real-binary run on three seeded data dirs:
    - A, promoted before the fix: re-minted before `listening on`. The marker equals the new id, `cloud_id` is kept, and there is one audit row.
    - B, never joined: untouched.
    - C, credentialed till: logged "deferred (joined_tills)".
    - On the second boot nothing re-minted.
    - `POST /api/sync/primary-proof` returned the new id on A and the old id on C.
  - mDNS is not observable in the container, so the advertised id was read from the DB and the proof API, which use the same source.
  - `-race` on data/app: no DATA RACE. The only failures were the pre-existing `TestStockUploadPendingAndHasSendable_Fast…` timing test, which also fails on `main`, and one SQLITE_BUSY flake that passes in isolation.

### Verdict
**Approve** with finding 1 fixed.

### Deferred
- Part B: promoted tills that already have joined tills keep the old id until #5904 lands. It blocks on an ADR-0114 §12 amendment.
- Finding 2 is noted there.

Pre-flight: web/help n/a (no user-visible change) · reference/ n/a (no reference page names the discovery id) · README n/a · en.json/i18n-meta n/a (no keys) · ADR updated (ADR-0033 "two till ids" sentence, ut-docs `docs/5897-discovery-id-repair`; amendment, so the index entry is unchanged) · mirrors n/a · review record: this file · follow-ups: #5904 (Part B).
