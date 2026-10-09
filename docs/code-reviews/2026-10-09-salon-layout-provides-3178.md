# Review: builtin layout-salon is found by `provides`, not by plugin ID (ut-docs#3178)

Branch `feat/3178-salon-provides`. ADR-0129 slice 4. Author: Sonnet (Dev
subagent, lane:cloud-54, complexity:easy). Independent review: Opus 5.5,
fresh context, in a separate worktree.

## What shipped

- `plugins/layout-salon/plugin.json` declares
  `"provides": ["layout.shop_type:service"]`. The version moves from 0.4.0 to
  0.4.1, so on a till that already has 0.4.0, `Sync`'s stale-version path
  reinstalls it and the stored `provides` rows match the binary.
- `internal/plugins/builtinlayouts`:
  - Keeps a list of embedded builtins (manifest bytes + locales FS; one entry
    today).
  - The layout for a shop type is the embedded builtin whose manifest provides
    `layout.shop_type:<type>`. Its ID is read from that manifest.
  - New exported `PluginIDForShopType` does this lookup. `Sync` loops over the
    builtins: it installs or upgrades the wanted one and removes any other
    installed one.
  - Only embedded manifests are read, so a marketplace plugin that declares
    `layout.shop_type:*` is never selected or removed (ADR-0129 §2).
  - `SalonPluginID` and `pluginForShopType` are gone.
  - `installSalon` and `removeSalon` are now `installBuiltin` and
    `removeBuiltin`. The locking (`syncMu`, `LockPluginTree`,
    `UninstallPluginTree`) and the `changed` contract are unchanged.
- The `#2879` line for builtinlayouts is removed from
  `scripts/ci/core-neutral-allowlist.txt`, and no Go file under `internal/`
  names the salon plugin ID any more.
- **New (review finding 1):** the stale-version reinstall keeps an
  operator's **disable** setting. Before this change, a version bump re-enabled
  a salon layout the operator had switched off in Settings → Plugins. This is
  the first salon bump to reach tills, so the bug would have been new to real
  tills.
- Tests that named the ID now resolve it through `PluginIDForShopType`
  (`builtinlayouts_test.go`, `internal/pages/{init,shop_type_layout,settings_page}_test.go`).
  Comments that still said `installSalon`/`removeSalon` are renamed.

## Tests

- `TestPluginIDForShopType_ResolvesEmbeddedSalonForService`: `service` returns
  the embedded salon ID, and every other shop type returns "".
- `TestEmbeddedSalonManifest_ProvidesServiceShopType`.
- `TestSync_LeavesNonEmbeddedShopTypeProviderAlone`: a `verified`-trust
  plugin that declares `layout.shop_type:service` stays installed through
  `Sync("service")` and `Sync("cafe")`.
- `TestSync_StaleVersionReinstall_KeepsOperatorDisable`: written first. It
  failed with "a reinstall over an operator-disabled builtin must keep it
  disabled", then passed after the fix.
- The reviewer re-verified the TDD claims in its own worktree. With the
  HEAD~1 `plugin.json`, the first two tests fail
  (`PluginIDForShopType("service") = ""`, and `got []`). With HEAD restored,
  they pass.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The version bump re-enables a salon layout the operator disabled | Fixed, with a test |
| 2 | minor | Comments still name `installSalon`/`removeSalon` (init.go, setup_page.go, settings_page.go, several tests) | Fixed |
| 3 | nit | `PluginIDForShopType` is exported mainly for tests in `internal/pages` | Accepted: it is the documented lookup for any caller that needs the ID |
| 4 | nit | The embedded manifest is re-parsed on each `Sync` | Accepted: it is cheap and keeps the code simple |

The reviewer confirmed:
- Behaviour is unchanged for every shop type.
- The `changed`/error contract still matches the three callers (setup,
  settings, the init reconcile).
- `PersistManifest` step 2b stores the salon's `provides` rows.
- The salon has no preset entry, so preset exclusivity is not involved.
- No new file writes and no cwd-relative paths. The locale write still uses
  `paths.Plugins` and `os.MkdirAll`.

## Verified

- `gofmt -l .` is clean. `go build ./...` and `go vet` pass on the touched
  packages.
- `go test ./internal/plugins/builtinlayouts/ ./internal/pages/ ./scripts/ci/...`
  passes.
- `go test ./internal/plugins/ -run 'Layout|Provides|Salon|Preset|Manifest'`
  passes. The full `internal/plugins` package timed out locally in unrelated
  WASM-compile/blob-quota tests, so CI's run is the gate for it.
- `guard-core-neutral.sh` passes (19 allow-list entries held), as do
  `guard-data-access.sh` and `golangci-lint` (0 issues).
- No visible surface changed: menu, rail and labels are the same, so nothing
  was looked at in a browser. CI's UI E2E (`layout-plugin-menu-1904.spec.ts`
  drives the real salon plugin) covers the layout at runtime.

## Verdict

Safe to merge once CI is green. Deferred: none. The fiscal and AI sites are
already their own cards (ADR-0129 slices 5–6, #3180).
