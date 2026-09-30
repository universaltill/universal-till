# Review: plugin install paths serialized with Rollback (ut-docs#3273)

**Date:** 2026-09-30 · **Lane:** lane:cloud-24 · **Branch:** `fix/3273-install-plugin-lock`
**Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent, separate worktree)

## What shipped

- `Importer.Import` (`internal/plugins/importer.go`) and
  `MarketplaceInstaller.installBundleFile` (`internal/plugins/installer_marketplace.go`,
  shared by the direct and staged download → install flows) now take the
  per-plugin lock `lockPluginTree(base, manifest.ID)`. It is the same lock
  that already serializes `Rollback`, `StoreVersion`, `UninstallPluginTree` and
  `RemoveVersionDir` (ut-docs#3035, #3082).
- The lock is taken after the manifest is verified and its id/version
  validated. It is held until return, like `Rollback`. It covers the live dir's
  `RemoveAll`/`Rename`, `PersistManifest`, the catalog upsert and the
  failure-path `RemoveAll(finalDir)`. So a Rollback of the same plugin can no
  longer interleave with the dir replace, or commit its version over a fresh
  install.
- Comments on `pluginLocks` / `lockPluginTree` now list the install paths and
  state that none of these functions may call another (sync.Mutex is not
  reentrant).

## Tests

- `TestImport_WaitsForConcurrentRollback` and
  `TestMarketplaceInstall_WaitsForConcurrentRollback`
  (`internal/plugins/install_lock_test.go`): each fires an install of plugin X
  at the `rollbackAfterTargetStat` hook of a Rollback of X. The install must
  not finish while the rollback runs, and must end as the active version.
  **TDD verified twice** (by the author and by the reviewer): without the lock
  both fail with "install of … finished while a Rollback of it held the plugin
  tree". The log showed the real damage: "rolled back from 3.0.0 to 1.0.0",
  meaning the rollback committed over the fresh import. With the lock both pass
  (reviewer: `-race -count=10`, 0 failures, no race reports).
- Gate: gofmt, `go build ./...`, `go vet ./internal/plugins/`, golangci-lint
  on `./internal/plugins/...` (0 issues). `go test ./internal/plugins/` passes
  on its own (399s).
- `-race` runs of `./internal/plugins` and `./internal/pages` on this
  contended 4-core container hit the 600s default timeout. CI runs those two
  packages in separate steps with a wider timeout for this reason
  (ut-docs#643/#753/#776). One timing test,
  `TestEventBus_Publish_ChannelFullDiagnosticThrottled`, failed under that
  load. It is event-bus code this diff doesn't touch; CI is the arbiter.
- CI guards run locally: green except the environment-only ones (no
  `shellcheck` binary; the deadcode guard skips the GTK desktop root here, which
  is what uses the two flagged `internal/logging` funcs).
- Not UI-facing: no driven run or screenshots needed.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix (pre-existing, out of scope) | `builtinlayouts.installSalon` writes the live salon dir and calls `PersistManifest` without the lock | Filed as ut-docs#3278 |
| 2 | nit | New test has no `starting` handshake before its 200ms window, unlike the sibling tests | Accepted: the timeout covers it, and without the lock the test fails deterministically |
| 3 | nit | Without the lock, detection depends on the install finishing within 200ms | Accepted: same limitation as the precedent tests |
| 4 | nit | `lockPluginTree` doc still named only UninstallPluginTree | Fixed |

The reviewer's reentrancy and lock-order audit was clean:
- Only 6 `lockPluginTree` sites exist. Nothing inside the locked regions
  (PersistManifest, upsertCatalogEntry, GrantPermission, checkDiskBudget,
  EnsureCatalogEntry) calls back into them.
- No caller installs while holding the lock: `plugin_api.go`'s StoreVersion
  returns before Install, and the cloudsync steps run one after another.
- The order is always lock → DB tx, and builtinlayouts `syncMu` → per-plugin
  lock is one-directional.
- No DB access happens before the lock, so a waiter never holds a SQLite tx.
- Every constructor uses `paths.Plugins()`. No new file write lacks
  `MkdirAll`.

## Verdict

Safe to merge.
