# Review: builtin salon layout install holds the per-plugin lock (ut-docs#3278)

**Date:** 2026-09-30 · **Lane:** lane:cloud-24 · **Branch:** `fix/3278-salon-install-lock`
**Author model:** Sonnet (easy card) · **Reviewer:** Opus 5.5 (independent subagent, fresh context, separate worktree)

## What shipped

- `plugins.LockPluginTree(base, id)` (`internal/plugins/rollback.go`): exported
  wrapper over `lockPluginTree`, the per-plugin mutex that already serializes
  Rollback, StoreVersion, UninstallPluginTree, RemoveVersionDir and both
  install paths (ut-docs#3035, #3082, #3273). The `pluginLocks` comment lists
  the new caller.
- `builtinlayouts.installSalon` takes it on `paths.Plugins()` + the salon id
  before writing the live locale dir and holds it through `PersistManifest`.
  A Rollback of layout-salon can no longer interleave with the shop-type
  reconcile's install and commit over it.
- Lock order stays one-directional: `syncMu` → per-plugin lock. `removeSalon`
  takes and releases the lock (via `UninstallPluginTree`) before `installSalon`
  runs, so they never nest.

## Tests

- `TestSync_SalonInstall_WaitsForPluginTreeLock`: with the lock held by the
  test, `Sync("service")` must not return within 200 ms, write the locale dir,
  or install the plugin; after release it completes and the plugin is
  installed at the embedded version. **TDD verified twice** (author and
  reviewer): with the `defer` removed it fails with `Sync returned (<nil>)
  while the plugin tree lock was held`; with it, it passes (reviewer: 5× under
  `-race`, no race reports).
- Gate: gofmt, `go build ./...`, `go vet ./internal/plugins/...`,
  golangci-lint (0 issues), `go test ./internal/plugins/...` green (root
  package 265–290 s), `internal/pages` plugin/layout/rollback tests green. The
  full root `internal/plugins` package under `-race` exceeded the 10-min local
  timeout on this container (slow runner; left to CI). CI guards run locally:
  green except environment-only ones (no `shellcheck` binary; the deadcode
  guard skips the GTK desktop root here and flags `internal/logging` funcs that
  root uses — unrelated to this diff, same as the #3082 record).
- Not UI-facing: no driven run or screenshots.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | The installed-version check and the remove→install gap on the reinstall path sit outside the lock; a Rollback could slip in. Harmless: the salon install never snapshots (`StoreVersion`), so Rollback finds no target; otherwise last-writer-wins and the next reconcile converges. | Accepted. |
| 2 | nit | On the (already failing) 5 s timeout branch the test returns while the Sync goroutine may still run into Cleanup. | Accepted — failure path only; the lock release in Cleanup prevents a hang. |
| 3 | nit | The 200 ms negative window could pass vacuously on a very slow machine; it can't fail spuriously. | Accepted — the TDD run proves it catches the bug. |

Reviewer also checked, no issue: `PersistManifest` takes no lock, runs no
hooks or reloads; no `Sync` caller holds the per-plugin lock and no lock holder
calls `Sync`; the lock key matches every Rollback caller in `internal/pages`
(`paths.Plugins()`, `filepath.Clean`ed); `os.MkdirAll` precedes the writes; no
SQL, money or user-facing strings in the diff.

## Verdict

Safe to merge.
