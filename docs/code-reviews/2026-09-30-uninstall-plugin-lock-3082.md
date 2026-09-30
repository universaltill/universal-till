# Review: plugin uninstall serialized with Rollback/StoreVersion (ut-docs#3082)

**Date:** 2026-09-30 · **Lane:** lane:cloud-54 · **Branch:** `fix/3082-uninstall-plugin-lock`
**Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent, separate worktree)

## What shipped

- `plugins.UninstallPluginTree(ctx, db, base, id)` (`internal/plugins/install.go`):
  validates the id, takes the per-plugin lock that already serializes
  `Rollback` and `StoreVersion` (`pluginLocks`, ut-docs#3035), then deletes the
  DB rows (`UninstallPlugin`) and best-effort `RemoveAll`s `base/<id>`.
- `plugins.RemoveVersionDir(base, id, version)`: removes one live
  per-version dir under the same lock. Used for the orphaned mismatched-version
  cleanup after the cloudsync auto-rollback (`cloudsync_wire.go`).
- `lockPlugin` now delegates to a package-level `lockPluginTree(base, id)`.
- All three uninstall callers use the helper: the Settings uninstall handler
  (`plugin_api.go`), `cloudRemovePlugin` (cloud `remove_plugin`, sync prune,
  mismatch fallback) and `builtinlayouts.removeSalon`.
- Behaviour otherwise unchanged: DB delete → file removal → install-status
  clear → plugin reload; file removal stays best-effort; the HTTP error text is
  unchanged.

## Tests

- `TestUninstallPluginTree_WaitsForConcurrentRollback`: an uninstall fired at
  the `rollbackAfterTargetStat` hook must wait for the rollback. **TDD verified
  twice** (author and reviewer): with `lockPluginTree` a no-op it fails with
  `Rollback raced with an uninstall of the same plugin: plugin
  com.test.uninstallrace has no active version`; with the lock it passes
  (reviewer: 3× under `-race`, no race reports).
- `TestUninstallPluginTree_RemovesRowsAndFiles`, `…_RejectsInvalidID`,
  `TestRemoveVersionDir` (removes only that version; rejects `""`, `.`, `..`,
  `../x`).
- Gate: `go build ./...`, `go vet ./internal/...`, gofmt, golangci-lint (0
  issues), full `go test ./...` green; CI guards run locally — green except
  environment-only ones (no `shellcheck` binary; deadcode guard skips the GTK
  desktop root here, which uses the two flagged logging funcs). Docs-shots
  surface hash refreshed (no rendered change: `Docs-Shots-Unchanged: true`).
- Not UI-facing: no driven run / screenshots needed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `cloudsync_wire.go` post-rollback `os.RemoveAll(<id>/<mismatched version>)` was still an unlocked tree mutation; a concurrent `StoreVersion` could snapshot a half-deleted dir. | **Fixed** — `plugins.RemoveVersionDir` under the lock + test. |
| 2 | nit | `pluginLocks` comment over-long after edit. | Fixed (rewrapped). |
| 3 | nit | Removal warning lost the full path. | Fixed (logs `base/<id>`). |
| 4 | nit | Lock wait ignores `ctx`. | Accepted, documented (bounded by one rollback). |
| 5 | nit, pre-existing | Install paths (`installer_marketplace.go`, `importer.go`) write the live version dir without the lock. | Out of scope → **ut-docs#3273**. |

Reviewer also checked, no issue: no reentrancy/deadlock (no lock holder calls
another lock taker or an uninstall; no lock held inside an open DB tx); lock
key matches (every `NewRollbackManager` and uninstall caller passes
`paths.Plugins()`, both `filepath.Clean`ed); `SalonPluginID` passes
validation; test timing is deterministic in the pass case; raw SQL only in
`_test.go` (guard-data-access passes); no file writes, nothing cwd-relative.

## Verdict

Safe to merge.
