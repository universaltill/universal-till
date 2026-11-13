# Review: revocation feed resolves the listing UUID (ut-docs#5653)

Date: 2026-11-05 · Branch: `fix/5653-revocation-feed-id`
Independent review by a different model than the author. Tester was a
separate agent.

## What shipped

The cloud revocation feed's `pluginId` is the marketplace listing UUID, but
the till matched it against `plugins.id` (the manifest id). A revocation never
disabled anything.

- `internal/plugins/revocation.go`:
  - `RevocationEntry.ManifestID` (`manifestId,omitempty`, ut-cloud contract
    0.0.11). The doc comment names both id kinds and the ut-docs contract page.
  - `resolveTarget` tries, in order: the feed's `manifestId`, the
    `plugin_install_status` row for the listing UUID (its `plugin_id`), then
    `pluginId` directly. The first candidate with a `plugins` row matching
    `version` wins. All lookups go through existing repo methods
    (`InstallStatusRepo.Get`, `PluginRepo.GetPlugin`), so there is no new SQL.
  - `processRevocation` stops, disables and audits the resolved id. The audit
    details add `listing_id` (the feed's `pluginId`) and `matched_by`. An
    entry that matches nothing is a Debug-level no-op.
  - `SetOnRevoked` hook: `SyncRevocations` calls it once, after a pass that
    disabled at least one plugin. A hook error is logged and never fails the
    sync.
- `internal/server/server.go`, `internal/app/app.go`: the hook is
  `Deps.ReloadPlugins`, so `WasmRuntime.Sync` drops the now-inactive module
  without a restart. It is nil when `deps` is nil.
- `internal/plugins/marketplace/client.go`: the same `ManifestID` field on the
  unused second decoder, to keep the two consistent.
- Tests (`internal/plugins/revocation_test.go`) cover: a listing UUID resolved
  through install_status, version mismatch, already inactive, unknown UUID, a
  blank install_status `plugin_id`, a stale install_status row, the direct
  manifest-id fallback, a preferred feed `manifestId`, the hook (called once,
  not called on a no-op pass, its error swallowed), and the wire decode with
  `manifestId`.
- Docs:
  - `web/help/en/plugins.md` step 15: a withdrawn plugin version is turned
    off at the next check and shows as **Disabled**, selling is never
    blocked, and enabling the same version again does not last.
  - `docs/data-model.md` "Revocation Endpoint": the field meanings and the
    resolution order.
  - Contract: ut-docs `reference/contracts/plugin-revocation-feed.md`.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | Manual not shipped with the behaviour (#324). A revoked plugin now really turns off, but `web/help/en/plugins.md` did not say so. | Fixed: help step 15 added. |
| 2 | Low | `docs/data-model.md` "Revocation Endpoint" said "revoked plugin IDs … and timestamp": wrong id kind, and there is no timestamp. | Fixed. |
| 3 | Low | If `ReloadPlugins` fails, the DB row is disabled but the WASM module stays loaded until the next reload or restart. The next poll finds the row inactive, so it does not call the hook again. | Accepted: the module is dropped at any later reload, and the failure is a logged warning. Documented on the contract page. |
| 4 | Low | `resolveTarget` queries install_status before it tries the feed `manifestId` candidate. An install_status read error therefore skips an entry that `manifestId` alone could have matched. | Accepted: a read error on one table means the DB is unhealthy; the entry is retried on the next poll. |
| 5 | Info | Audit `listing_id` holds the feed's `pluginId`, which is a manifest id when an old-style feed matched by `plugin_id`. | Accepted: `matched_by` says which case applies. |

Security checks (no finding):
- Revocation only disables. The verify path (ADR-0006) is untouched.
- Every lookup is parameterised. A malformed or empty id matches nothing.
- SQLite `=` is case-sensitive, so a re-cased `manifestId` misses rather than
  hits another plugin.
- `plugins.id` is the primary key and the version must match exactly, so an
  entry cannot disable a different plugin than the one its listing or
  manifest id names.
- A wrong `manifestId` could only come from ut-cloud itself: slug is unique
  and is set from the manifest id at first publish.

Reload hook (no finding):
- `ReloadPlugins` already runs from the background sync-pull goroutine. It
  takes `PluginMu` itself and runs the chip refresh after the unlock.
- The hook is set before `Start` launches the goroutine, so there is no race.
- It runs only on the revocation goroutine and never on a sale path. A fetch
  failure returns before the loop. Offline-first is unchanged.

## Extra verification

- Re-run here:
  - `gofmt -l .`: clean. `go build ./...`: ok.
  - `go vet ./internal/plugins/ ./internal/server/ ./internal/app/`: ok.
  - `go test -count=1 -run 'Revocation' ./internal/plugins/`: ok.
  - `go test ./internal/server/... ./internal/app/...`: ok.
  - `go test ./internal/manual/...`: ok.
  - `golangci-lint run ./...`: 0 issues.
  - `guard-help-topics.sh`, `guard-help-terms.sh`, `guard-compliance-claims.sh`,
    `guard-competitor-naming.sh`, `guard-mail-addresses.sh`: exit 0.
  - `guard-help-drift.sh`: only pre-existing, non-blocking warnings.
- TDD claim re-verified in a scratch copy (`mktemp -d`; the real tree was
  untouched). With the install_status candidate neutralised,
  `TestSyncRevocations_ListingUUIDResolvesViaInstallStatus` fails with
  `n = 0, want 1`. It passes on the real tree.
- Tester, in a separate scratch copy:
  - A semantic mutant of the old behaviour fails the ListingUUID,
    PrefersFeedManifestID and InvokesOnRevoked tests.
  - Driven run: ut-cloud's real protojson output, served by httptest, against
    a migrated SQLite DB. It gave n=2 and one hook call. The plugin matched by
    `manifest_id` and the one matched by `listing_id` were both disabled, an
    unrelated plugin was untouched, and the second poll gave n=0 with no hook
    call.
  - Not driven: the real `WasmRuntime` unload (confirmed by reading the code,
    `wasm_runtime.go` Sync drops `!IsActive` rows) and a full app boot with a
    live marketplace.
- Caveat: `go test -race -timeout 40m ./internal/plugins/...` timed out in this
  sandbox. wazero compiles take about 23s per module under `-race` on 4 CPUs.
  It ran with no FAIL and no DATA RACE line before the timeout, but that is not
  a pass. Without `-race`, `go test -timeout 90m ./internal/plugins` passed:
  709 PASS, 0 FAIL, 771s.

## Verdict

Approve. The fix resolves the listing UUID correctly, it fails safe (it only
disables, and only on an exact id and version match), the repository pattern
holds, and the ut-cloud mirror test pins the wire field set.

## Deferred (Backlog cards)

- Act on `action: "delete"` (a real uninstall).
- Use the `latestVersion` / `since_version` cursor. The full feed is fetched
  every 30 minutes.
- Block re-enabling or cloud re-installing a revoked version. Today the next
  poll turns it off again.
- A merchant-visible "plugin revoked" status chip.
- Cap the size of the feed response body (`io.LimitReader`), which is
  pre-existing.

Pre-flight: till user-visible behaviour: `web/help/en/plugins.md` updated (no
UI changed, so no docs-shots needed); `reference/` updated (ut-docs
`reference/contracts/plugin-revocation-feed.md` new, `plugin-lifecycle.md`
link, till `docs/data-model.md`); README n/a (does not describe revocation);
en.json n/a (no new keys); ADR n/a (contract bug fix inside ADR-0006, no new
decision); manifest field / host function / plugin type n/a (the feed is not
the manifest; ut-cloud mirror test added for the feed); review records
updated (this file, ut-cloud, ut-docs); follow-ups listed above as cards.
