# Review — plugin rollback snapshots: lock, keep the installed version, no bad-version snapshot (ut-docs#3035)

**Branch:** `fix/3035-rollback-snapshot-protection` · **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent, isolated worktree)

## What shipped

`internal/plugins/rollback.go` closes the three gaps left after #3032:

1. **Concurrent eviction.** A package-level per-plugin mutex (`pluginLocks`,
   keyed by the cleaned plugin base dir + plugin id, because `internal/pages`
   builds a fresh `RollbackManager` per call) is held by `StoreVersion` and
   for the whole of `Rollback`. A StoreVersion from the update or sync path
   can no longer evict the target between Rollback's stat and its manifest
   open.
2. **Active snapshot evicted next.** `cleanupOldVersions` always keeps the
   plugin's installed version (`GetInstalledPluginVersion`, active or
   disabled). If that read fails it evicts nothing and returns an error
   (logged as a warning by the caller). An extra snapshot is cheaper than
   losing the active one.
3. **Known-bad version takes a slot.** New `RollbackDiscardingCurrent` does not
   snapshot the version it leaves. `cloudsync_wire.go`'s pinned-version-mismatch
   auto-rollback uses it, because it deletes that version straight afterwards.

The "touch the target's mtime" option was not taken. Gap 2 is covered by
keeping the installed version, and touching the mtime would change
`InstalledAt` in the Versions list.

Docs: `docs/data-model.md` notes that the installed version is never evicted.
`web/help/img/manifest.json` has only a surface-hash refresh
(`Docs-Shots-Unchanged: true`): the `internal/pages` edit is backend-only and
changes no rendered pixel.

## Tests (each failed on `main`/without its fix first)

| Test | Failure without the fix |
|---|---|
| `TestRollback_ConcurrentStoreVersionCannotEvictTarget` | `failed to open manifest: …/versions/1.0.0/manifest.json: no such file or directory` |
| `TestStoreVersion_NeverEvictsActiveVersionSnapshot` | active version's snapshot evicted |
| `TestStoreVersion_DBErrorSkipsEviction` | `1.0.0` evicted with the active version unknown |
| `TestRollbackDiscardingCurrent_DoesNotSnapshotTheVersionLeft` | `versions/9.9.9` created |
| `TestSyncPullTick_VersionMismatchOnUpgradePreservesPriorGoodVersion` (new assertion, end to end through the sync tick) | `versions/9.9.9` snapshotted on the replica |

The author checked all five directions (the fix stubbed out, then restored).
The reviewer checked them again on its own, with four mutants (lock removed,
installed-keep removed, DB error made non-fatal, discard flag flipped). Every
new test failed on its mutant and passed on the fix.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `StoreVersion` took the lock before validating the id, so a garbage id got a never-freed lock entry | **Fixed**: validate first |
| 2 | minor | The concurrent test's 200 ms window could let a lock-removal mutant survive on a starved box (it can never fail spuriously) | **Fixed**: the goroutine signals it has started before the grace window |
| 3 | minor | `cleanupOldVersions` uses `context.Background()` (the public `StoreVersion` signature is frozen) | Accepted: a single point query, bounded by SQLite `busy_timeout` |
| 4 | nit | The lock key uses `filepath.Clean`, not an absolute path | Accepted: every caller passes `paths.Plugins()` |
| 5 | out of scope | `cloudRemovePlugin`, `GetVersionHistory` and `HasVersion` are not under the lock, so an uninstall racing a rollback is still unserialized | Pre-existing, not one of #3035's gaps. Filed as ut-docs#3082 (Backlog) |

Deadlock analysis (reviewer): none of the four production call sites holds a
`*sql.Tx` or the plugin lock when it calls in. `rollback()` calls the private
`storeVersion`, so the lock is never taken twice. `cleanupOldVersions`' DB read
runs only after `tx.Commit()`. The SQLite pool has no `SetMaxOpenConns` cap,
and it sets `busy_timeout(5000)`.

## Verified

- `go build ./...`, `go vet`, `gofmt -l` clean. `golangci-lint run ./...`: 0 issues.
- `go test -race` for `internal/plugins` and `internal/pages` (reviewer and
  author): ok.
- CI guards in `ci.yml`'s build job pass locally, with these exceptions:
  - `guard-shellcheck-version`: no shellcheck binary in this container.
  - `guard-deadcode-baseline`: it flags `internal/logging/file.go`, which this
    PR does not touch. It is used only from `cmd/unitill-desktop`, which the
    container can't build (no GTK headers). CI analyzes that root.
- There is no UI surface, so no driven run and no screenshots.

## Verdict

Safe to merge.
