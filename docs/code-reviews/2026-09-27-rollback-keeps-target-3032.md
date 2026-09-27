# Review: plugin rollback no longer evicts its own target (ut-docs#3032)

**Date:** 2026-09-27 · **Lane:** cloud-24 · **Complexity:** easy
**Author:** Sonnet (dev subagent) · **Reviewer:** Opus 5.5 (fresh-context subagent, isolated worktree)

## What shipped

`internal/plugins/rollback.go`:

- `Rollback` now snapshots the version it leaves only **after `tx.Commit()`**.
  Before this change the snapshot ran before the target's manifest was opened,
  and its `cleanupOldVersions` pass (keep `maxVersions`=3, oldest-by-mtime
  evicted) could delete the rollback target itself. A rollback to the oldest
  snapshot then failed with `failed to open manifest … no such file or
  directory`. A rollback later refused by a validator also used to add a
  snapshot and evict one.
- `cleanupOldVersions(pluginID, keep ...string)` never deletes a name in
  `keep`. It trims the other candidates, oldest first, down to `maxVersions`
  where possible. A keep set larger than `maxVersions` is kept whole.
- The unexported `storeVersion(…, keep ...string)` carries the keep set. The
  public `StoreVersion` signature is unchanged, so its callers in
  `internal/pages` are untouched. `Rollback` passes the target as the keep
  name.

## Tests (`internal/plugins/rollback_test.go`)

- `TestRollback_ToOldestSnapshotKeepsTarget` fails on `main`:
  `failed to open manifest: …/versions/1.0.0/manifest.json: no such file or directory`.
  It also fails when the snapshot is moved after commit but the keep argument
  is dropped (the reviewer checked this).
- `TestRollback_RefusedDoesNotTouchVersionsDir` fails on `main`:
  `versions/0.8.0 was removed by a refused rollback`. It compares the set of
  names, so the "+1 −1, same count" case can't slip through.
- `TestCleanupOldVersions_KeepProtectsFromEviction` checks the keep-set
  eviction choice directly.

The reviewer re-verified the TDD claims in an isolated worktree: both tests
fail with the original `rollback.go` and pass with the fix.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A crash between commit and the post-commit snapshot leaves no roll-forward snapshot. The live dir of the version just left is still on disk, so nothing is lost. | Accepted. This trade-off is better than the old one, where refused rollbacks mutated `versions/`. |
| 2 | minor (existed before) | `StoreVersion` from update or sync passes no keep set: a concurrent call can still evict a rollback target mid-rollback. The active version's snapshot also stays the oldest by mtime, so it is evicted next. | Deferred → ut-docs#3035 |
| 3 | nit (existed before) | The cloudsync auto-rollback snapshots the mismatched version it then deletes, so that snapshot takes a slot. | Deferred → ut-docs#3035 |
| 4 | nit | A comment in `storeVersion` said Rollback "below" guards "its own StoreVersion call". | Fixed: now "above", post-commit `storeVersion`. |

Checked with no problems found:
- the count math in `cleanupOldVersions`: empty keep, keep larger than max, keep naming a missing dir;
- `currentVersion != targetVersion` is enforced, so the version being snapshotted is never the kept one;
- no file write lacks `os.MkdirAll`, and there are no cwd-relative paths;
- no SQL was added outside `internal/data`.

## Verification

- `gofmt -l .` shows nothing, `go build ./...` passes, `go vet ./internal/plugins/` is clean, and `golangci-lint run ./internal/plugins/...` reports 0 issues.
- `go test ./internal/plugins/ ./internal/pages/` passes.
- `go test -race` on every Rollback/StoreVersion/cleanup/HasVersion/GetVersionHistory test passes.
- Full-package `-race` runs of `internal/plugins` and `internal/pages` hit the sandbox time budget inside unrelated slow tests, so CI runs the full `-race` gate.
- These guards pass locally: data-access, core-neutral, kiosk-engine, i18n, compliance-claims, competitor-naming, page-http-error, plugin-menu-read, plugin-settings-bump.
- The deadcode-baseline guard fails locally on two `internal/logging` functions. This is a local-only artifact: `cmd/unitill-desktop` is skipped because the GTK headers are missing here. This diff does not touch that code.

No user-visible text, UI or help topic changed.

**Verdict:** safe to merge.
