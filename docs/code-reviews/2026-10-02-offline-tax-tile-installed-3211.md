# Review — setup wizard: no tax-plugin tile when one is already installed (ut-docs#3211)

- **Branch:** `fix/3211-offline-tax-tile-installed`
- **Lane:** `lane:cloud-24` · complexity: medium · built on Opus 5.5, reviewed independently by Fable
- **Date:** 2026-10-02

## What shipped

`setupInstallableTaxPlugin` (`internal/pages/setup_tax_catalog.go`) returned
the Offline tax-plugin tile before any install check when the catalog was
unreachable, so an offline wizard on a till that already had a tax plugin
(sideloaded from a file, restored DB, wizard re-run after a reset) still
prompted, and the tile's consent POST or Skip/Next queued a spurious install
(Settings pending chip then showed until the retry reached the catalog).

- New neutral repository method `PluginRepo.HasActiveEntryTypeForMarket(ctx,
  entryType, market)` (`internal/data/plugin_provides_repo.go`): an active
  plugin with an active `plugin_entries.type = entryType` entry whose
  ADR-0129 `plugin_markets` list the market, or are empty (every market,
  ADR-0129 §3). No plugin ids or country list in core (#2848).
- The card asked for "declares locale X"; the till persists neither a
  plugin's canonical type nor its locales, so the check uses the entry type
  plus ADR-0129 markets — the mechanism that already exists for "where a
  plugin is for". `ut-plugin-tax-de`'s manifest declares a `tax` entry
  (checked) and no markets, so it matches for DE.
- The wizard asks it **first**, before the catalog, so it covers both the
  offline tile and the online one (review finding 1). DB error fails open to
  still prompting.
- Help topic `users.md` step 8 (en, de, tr, fa) says the step doesn't ask
  when a tax plugin for the country is already installed. `make docs-shots`
  re-run; only `manifest.json` topic/surface hashes change (no screenshot of
  this state; unrelated `country-settings` PNG re-renders were discarded).

## Independent review (Fable) — findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | Help said "online or offline", but the check only ran offline; online, a sideloaded tax plugin left the tile up (install-status store is keyed by listing id) and consent attempted a second fiscal install | **Fixed**: check moved before the catalog fetch; new test `…OnlineWithSideloadedTaxPluginReturnsNil` (red on the offline-only version, green after) |
| 2 | minor | `basePluginRetryTick`/`resolveAndInstallBasePlugin` lack the same guard: a tax spec queued before a sideload/restore retries forever | Pre-existing; **follow-up Backlog card** filed |
| 3 | minor | `install_state='broken'` active plugin counts as present | **Accepted + documented** in the method doc (a second install would collide with fiscal exclusivity; broken state is surfaced and tax fails closed elsewhere) |
| 4 | minor | Repo test didn't really test the upper-casing | **Fixed**: `check("tax","fr",true)` before any no-markets plugin exists |
| 5 | minor | Detection relies on a `tax` entry | **Documented** in the method doc; verified against `ut-plugin-tax-de/manifest.json` |
| 6 | nit | `plugin_markets` created ad hoc in the repo test | Accepted (one test; mirrors other ad-hoc schemas in `plugin_repo_test.go`) |
| 7 | nit | Fail-open path untested | **Fixed**: `…LocalCheckErrorStillPrompts` (drops `plugin_markets`, asserts the Offline tile) |

## Verified

- TDD: the reviewer reverted the fix and saw `…CatalogUnreachableWithActiveTaxPluginReturnsNil`
  and `…OfflineWithActiveTaxPluginShowsNoTileAndQueuesNothing` fail with the
  real symptom (offline tile rendered, consent redirect `&tax_plugin_pending=1`),
  then pass after restoring. The orchestrator did the same for the online test.
- Handler level through the real mux and migrated DB: offline DE render has no
  tile; consent POST redirects without `tax_plugin_pending`; Skip queues
  nothing (pending list empty).
- `gofmt`, `go build ./...`, `go vet ./internal/...`, `go test ./...` (all
  green), `golangci-lint run ./...` (0 issues), guards: data-access,
  core-neutral, i18n, help-drift, help-topics, compliance-claims,
  competitor-naming, card-data-schema, kiosk-engine, no-showmodal,
  page-http-error, plugin-menu-read, docs-shots.
- Not done: no browser-driven run of a sideloaded-plugin first boot (the
  render is server-side and asserted at the handler level; the tile simply
  isn't rendered). `guard-deadcode-baseline` reports `internal/logging`
  Stderr locally only because this container lacks GTK headers to analyse
  `cmd/unitill-desktop` — unrelated to this diff.

## Verdict

Safe to merge.
