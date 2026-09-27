# Review — plugin-update chips refresh after every plugin change (ut-docs#2787)

Date: 2026-09-27 · Branch: `fix/2787-pending-updates-refresh` · Lane: cloud-54
Built by Sonnet (card `complexity:easy`); reviewed independently by Opus 5.5.

## What shipped

- `(*common.Deps).RefreshPendingUpdates` (`internal/pages/common/pending_updates.go`):
  recomputes the "Plugin updates available (N)" / "Language pack update
  available" status from the **local** catalog cache (`CatalogRepository.Get`
  — memory or disk, never the network) and publishes it. Count-only: it never
  applies anything. Same steady-state rules as `pluginUpdateCheckTick`: a
  main/standalone till doesn't count a language-pack update (the scheduler
  auto-applies it); a joined till counts it, sets `LanguagePending` and
  carries `MainTillURL`. No-op when `CatalogRepo`/`Db` is nil or the catalog
  read fails.
- `Deps.ReloadPlugins` defers it as its first defer, so it runs after
  `PluginMu` is released and also when `Pm` is nil. Every lifecycle path goes
  through `ReloadPlugins`: manual install/update/uninstall/rollback/import,
  store install, cloud-directed install/uninstall, sync follow, layout sync.
- `plugins.NotePendingUpdateApplied` (the decrement after a manual update) is
  removed — the refresh now publishes the exact count, and keeping the
  decrement would under-count by one.
- Shared `plugins.CanonicalTypeLanguage` constant for the scheduler and the
  refresh.

The replica sync-follow path the card reported was already fixed by
ut-docs#2984 (`47ba234`); its explicit `pluginUpdateTickFn` call is left as is.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `..._MainTillAutoAppliesLanguage` never published a stale count first, so it passed with the refresh removed. | Fixed: it now publishes `{1, LanguagePending}` first; fails without the fix. |
| 2 | minor | On a main till, a language pack whose auto-apply failed (tick counted it) drops off the chip at the next `ReloadPlugins` until the next tick (≤15 min). | Accepted, documented in code: under-counting briefly is the safer direction; main tills only reload on plugin changes. |
| 3 | minor | A tick that read `found` before a manual action and publishes after it can overwrite the fresher refresh. | Accepted: pre-existing window (the old decrement had it too); next reload/tick corrects. |
| 4 | nit | Each reload may re-read the snapshot JSON from disk and `UpdateChecker` logs one Info line per available update; `convergePluginSet` reloads per plugin. | Accepted: small cost, log volume only. |

Also checked and fine: lock ordering (no `ReloadPlugins` caller holds
`PluginMu` or the catalog lock; refresh runs after the unlock), no network on
the path (offline-first holds, checkout never blocked), boot cost (one disk
read), removal of the decrement (with no `CatalogRepo` nothing ever publishes,
so nothing to decrement), no SQL outside `internal/data`, no user-facing
strings, no file writes.

## Verification

- TDD re-verified by the orchestrator: with the `defer` removed, 6 tests fail
  (install landed latest, rollback, uninstall, main-till language, joined-till
  language, uninstall handler); restored, all pass. The nil-`CatalogRepo`
  no-op test passes both ways by design.
- Handler layer: `TestUninstallHandler_RefreshesPendingUpdatesChip` drives
  `POST /api/plugins/{id}/uninstall` through the real mux after a real
  scheduler tick published Count=2; the chip drops to 1.
- Gate: gofmt clean, `go build ./...`, `go vet`, `golangci-lint run ./...`
  (0 issues), `go test ./...`, `go test -race ./internal/pages/common/`, the
  data-access / i18n / core-neutral / kiosk-engine / help-topics guards.
- Not driven in a running till: no UI or template change (the chip template
  is untouched); the behaviour is covered at the handler level.

## Verdict

Safe to merge.
