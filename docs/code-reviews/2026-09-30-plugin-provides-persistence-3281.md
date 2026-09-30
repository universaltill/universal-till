# Review: plugin `provides`/`markets` persistence and `fiscal.*` exclusivity (ut-docs#3281)

- **Date:** 2026-09-30
- **Card:** ut-docs#3281, ADR-0129 slice 2b (lane:cloud-54)
- **Author:** Opus 5.5 (pipeline build lane). **Reviewer:** Fable, independent subagent in a separate worktree
- **Branch:** `feat/3281-plugin-provides-persistence`

## What shipped

- Migration `053_plugin_provides_markets.sql` adds two tables, `plugin_provides(plugin_id, capability)` and `plugin_markets(plugin_id, market)`. Each has PK `(plugin_id, value)`, an FK to `plugins(id) ON DELETE CASCADE`, and an index on the value. The checksum is pinned in `shipped_migrations_test.go`.
- `internal/data/plugin_provides_repo.go` adds `ReplacePluginProvides`, `ReplacePluginMarkets`, `PluginsProviding(ctx, capability, activeOnly)` (sorted IDs), `ListPluginProvides` and `CapabilityProviderOwner`. The last one looks at installed plugins, active or not. All raw SQL for this lives here.
- `internal/plugins/manifest_provides.go` adds `IsExclusiveCapability` (every `fiscal.*` value), `FiscalProvidesConflict`, `validateFiscalProvidesExclusivity` and `persistProvidesAndMarkets`.
- `PersistManifest` runs the exclusivity check (step 0i) before any write and replaces both lists after the plugin row is written. `Rollback` does the same check and rewrites the lists from the target manifest. `POST /api/plugins/{id}/enable` returns 409 when another installed plugin provides the same `fiscal.*` capability, and 500 (fail closed) on a DB error.
- The two tables are classified non-admin in `sync_admin_repo.go`. They are recreated on re-install, like `plugin_entries`.
- Docs (ut-docs): `reference/plugin-manifest.md` and `architecture/plugin-architecture.md`.

**Split out, not in this PR:** the installer's real `min_pos_version` check is now ut-docs#3288 (`blocked:dep`). The first-party plugins declare `min_pos_version: "1.0.0"` while the till's release line is 0.30.x. A real `buildinfo` comparison would refuse every one of those installs.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | `TestSchemaTablesAreClassified`: the new tables were in neither `adminTables` nor `nonAdminTables`. | Fixed: non-admin, same reasoning as `plugin_entries`. |
| 2 | blocker | The hand-written `setupTestDB` schema (`internal/plugins/manifest_test.go`) had no `plugin_provides`, so 37 existing tests failed with `no such table`. The first full gate showed this, but I misread its summary. | Fixed: both tables added to the test schema. |
| 3 | should-fix (tracking) | Legacy tax-de/tax-tr installs have no `provides` rows, so exclusivity can't see them until the ADR-0129 §5.4 shim exists. | Accepted for this slice; the shim is slice 6. The requirement to wire the shim into `FiscalProvidesConflict` is recorded on ut-docs#3180. The risk is limited because ut-cloud accepts `fiscal.*` only from the first-party vendor. |
| 4 | nit | The migration comment named a method that doesn't exist (`FiscalCapabilityOwner`). | Fixed. Comment-only, so the checksum is unchanged (`internal/db` green). |
| 5 | nit | `layout.shop_type:*` is stored for non-embedded plugins. | Deferred to the slice-6 reader; recorded on ut-docs#3180. |
| 6 | nit | The 500 body includes the raw DB error text. | Kept, for consistency with the fiscal-sign and preset groups. No secrets reach it. |

The reviewer confirmed:
- The check runs inside the transaction before any write, and `_txlock=immediate` serialises installs, so there is no gap between check and write.
- There is no bypass path. The marketplace installer (including cloud-sync-driven replica installs), the importer and `builtinlayouts.Sync` all go through `PersistManifest`.
- A plugin's own rows are excluded, so self-update and re-enable work.
- Binding a `bool` in modernc works on both branches.
- The FK cascade and the migration are safe on existing tills: those tills get empty tables.

## CI follow-up

`desktop-shell`'s `guard-deadcode-baseline.sh` flagged `PluginRepo.PluginsProviding` and `PluginRepo.ListPluginMarkets` as unreachable, because no production reader exists until slice 6.
- `ListPluginMarkets` is removed. Only the tests used it, and they now query `plugin_markets` directly.
- `PluginsProviding` is the API the card asks for, so it goes into `scripts/ci/deadcode-baseline.txt`. ut-docs#3180 is asked to drop that entry once its readers call it.

## TDD re-verification (by the reviewer, revert → fail → restore)

- Neutralising the `PersistManifest` check makes `TestPersistManifest_RefusesSecondFiscalProvider` (both subtests) and `…DisabledFiscalProviderStillHoldsExclusivity` fail.
- Neutralising the `Rollback` check makes `TestRollback_RefusesRestoringASecondFiscalProvider` fail.
- Short-circuiting the enable 409 makes `TestFiscalProvidesExclusivity_EnableRefusesSecondProvider` fail (the plugin was enabled with a 200).
- I watched the new enable tests fail against the unfixed handler (200s) before implementing.

## Verification beyond unit tests

- `go build ./...`, `go vet ./internal/...`, `gofmt -l .` (clean), `golangci-lint run ./...` (0 issues). Full `go test ./...` rerun after the fixes.
- Every `bash scripts/ci/*.sh` guard in `ci.yml`'s `build` job passed, except `guard-shellcheck-version.sh` (no shellcheck binary in this container; no shell script changed).
- Playwright e2e passed: `layout-plugin-menu-1904.spec.ts` (installs and enables a layout plugin through the real app) and `catalog-row-oob-1363.spec.ts`, 11/11.
- No UI surface changed (backend and API error text only), so nothing needed a visual check.

## Verdict

Safe to merge once the full gate is green on the fixed head.
