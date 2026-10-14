# Review: drop the dead local plugin_catalog read path (ut-docs#2238)

Date: 2026-10-14 · Branch: `fix/2238-drop-dead-plugin-catalog-read` · Card: ut-docs#2238

## What shipped

Option (a) of the card: delete the unread local `plugin_catalog` read chain.
Plugins available to install are listed from the marketplace
`CatalogRepository` snapshot (`plugins_store_page.go`, `plugins.IndexCatalog`)
and installed ones from `PluginRepo.ListInstalledPlugins`; nothing read the
local mirror back.

- `internal/plugins`: removed `Manager.Catalog`, `loadCatalog` (its call in
  `Init`), `Manager.CatalogList`, `CatalogEntry`/`CatalogView`, `parseTags`.
- `internal/data`: removed `PluginRepo.ListCatalog`, `PluginRepo.CatalogPage`,
  `CatalogRow`.
- Tests: removed `TestParseTags`, `TestCatalogPage`,
  `TestPluginRepo_ListCatalog_ExcludesDeprecated` and the `CatalogList`
  assertions in `TestManagerInitAndReload` (the catalog-only seed stays, now
  documented as FK parent only). Two test comments that justified seed
  values by `ListCatalog`'s scan were reworded.
- `scripts/ci/deadcode-baseline.txt`: dropped `Manager.CatalogList` and
  `PluginRepo.CatalogPage` (burn-down, ut-docs#1566).

Kept: the write paths — `EnsureCatalogEntry` (satisfies `plugins(id,version)`'s
FK) and `UpsertCatalogEntry` — untouched and still tested. No migration
touched.

## Review

Independent review by a different model than the author (Fable; built on
Opus). No blockers or majors.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `loadCatalog` ran only in `Init`, never `Reload` — the old doc comment ("Init/Reload") was wrong and `m.Catalog` went stale after every install; further evidence it was dead. | Noted here; no code change. |
| 2 | minor (favourable) | Dropping `loadCatalog` removes a startup failure mode: it scanned nullable `description/author/website` into `string`, so one NULL row failed `Init`. Nothing relied on that error as a health check; nothing read `Manager.Catalog` via templates/JSON. | Noted; accepted. |
| 3 | nit | `com.test.c` seed still carried tags nothing parses. | Fixed (seed tags now empty). |
| 4 | nit | WIP commit message; review record missing. | Fixed (this record, real commit message). |

## Verified

- `gofmt -l` clean; `go build ./...`; `go vet` on plugins/data/pages;
  `golangci-lint run` on plugins/data/pages: 0 issues.
- `go test ./internal/data/` ok; `./internal/pages/` ok; `./internal/plugins/`
  ok with a wider timeout (1295s alone; CI runs it in its dedicated
  wider-timeout step — the default 600s is a pre-existing load issue).
- Repo-wide grep (Go, `web/`, `e2e/`, `mobile/`, `cmd/`, `scripts/`): no
  remaining reference to any removed symbol.
- `guard-deadcode-baseline-growth.sh`: removals only, passes.
  `guard-deadcode-baseline.sh` (local, without the GTK-only
  `cmd/unitill-desktop` root): no new unreachable function from this diff;
  its only hits (`internal/logging` `Stderr`, `timestampWriter.Write`) are
  the known skipped-root artefact, called from `cmd/unitill-desktop`.
- `guard-data-access.sh` passes.
- No docs describe the local catalog listing (`reference/data-model.md`
  lists `plugin_catalog` only as an install write target — still accurate).
  No user-visible change, so no manual/help update.

## Verdict

Safe to merge.
