# Code review — plugin rollback snapshot with a missing live dir (ut-docs#2799)

**Date:** 2026-09-27 · **Lane:** lane:cloud-24 · **Complexity:** easy
**Author:** Sonnet (dev subagent, two rounds) · **Reviewer:** Opus 5.5 (fresh context, two passes)

## What shipped

A till logged `plugin sync: failed to snapshot com.universaltill.language-de@1.1.24 before pinned install … failed to copy…` at WARN, which reaches the owner's problems list. `RollbackManager.StoreVersion` removed the existing `versions/<v>` snapshot *before* copying from the live `<plugins>/<id>/<v>` dir, so a missing live dir destroyed a good snapshot. The sync path then treated the plugin as having no rollback target, and a later version mismatch did a full uninstall instead of a rollback.

- `internal/plugins/rollback.go`
  - `StoreVersion` stats the source first. A missing source or a non-directory returns the new sentinel `ErrVersionSourceMissing` and leaves the snapshot alone. Other stat errors stay plain errors.
  - `StoreVersion` copies into `versions/.store-*` and swaps it in only after a successful copy, so any copy failure keeps the old snapshot.
  - New `HasVersion(id, v)`: true only when `versions/<v>/manifest.json` is a regular file.
  - `Rollback` restores a missing live `<id>/<target>` dir from the snapshot (temp dir + rename). Every runtime reader loads from the live dir, never from `versions/`.
- `internal/pages/cloudsync_wire.go` (`cloudInstallPluginVersion`): a missing source with a snapshot keeps the rollback target and logs at Info. A missing source with no snapshot logs at Info in plain words. Any other error keeps the WARN.
- `internal/pages/plugin_api.go` (manual update): a missing source logs at Info. Other errors now go through `logging.L().Warnf` instead of stdlib `log`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1-1 | major | Round 1 kept a rollback target whose live dir was missing. `Rollback` only rewrote DB rows, so the plugin stayed "installed" with no files, and a language pack silently lost its locales. The sync test didn't check files. | Fixed: `Rollback` restores the live dir. The sync test now asserts that `<id>/1.0.0/manifest.json` exists after the tick. |
| R1-2 | minor | Any copy failure (for example a symlinked source root, an unreadable file or a full disk) still wiped the snapshot. | Fixed: copy to a temp dir, then swap. New test covers it. |
| R1-3 | minor | Every stat error was mapped to the sentinel, which hid real I/O and permission faults at Info. | Fixed: only not-exist or not-a-directory maps to the sentinel. New test covers it. |
| R1-4 | minor | `HasVersion` accepted an empty or partial snapshot dir. | Fixed: it now requires `manifest.json`. |
| R1-5 | nit | Mixed stdlib `log` and `logging` in the update path. | Fixed. |
| R2-1 | minor | If a crash leaves `versions/.store-*` behind, `GetVersionHistory` lists it and `cleanupOldVersions` counts it towards the limit of 3. Picking it in the UI fails safely. | Accepted as a follow-up card. It needs a crash at one exact point and is less harmful than the old partial dir. |
| R2-2 | minor | `Rollback` restores the live dir before the manifest validators run. A refused rollback leaves orphan files. The DB stays consistent, and the installer or an uninstall overwrites or removes them. | Accepted as the same follow-up card. |

## TDD re-verification (reviewer, in a separate worktree)

The reviewer reverted the production files and ran each new or changed test. Each one failed with the real bug:
- `TestStoreVersion_MissingSourceKeepsExistingSnapshotIntact` failed at runtime once a stub made it compile: the error did not wrap the sentinel, and the snapshot was destroyed.
- `TestStoreVersion_SourceIsFileNotDirReturnsSentinel` failed.
- `TestStoreVersion_CopyFailureKeepsExistingSnapshotIntact` failed with "existing snapshot must survive a failed copy, but it's gone".
- `TestStoreVersion_OtherStatErrorIsNotSentinel` failed.
- `TestHasVersion` failed on the empty-dir case.
- `TestRollback_RestoresMissingLiveDirFromSnapshot` failed because the live dir was not restored.
- `TestSyncPullTick_VersionMismatchRollsBackEvenWhenLiveSourceDirMissing` failed at runtime against the original code with the exact field WARN and "snapshot must survive". Against round 1 it failed with "live install dir … to be restored".

All of them pass on the final code.

## Gates

- `gofmt -l .` printed nothing.
- `go build ./...` and `go vet` on the touched packages are clean.
- `go test ./internal/plugins/` and `go test ./internal/pages/` pass. The `-race` run of both packages also passed on round 1 but takes about 50 minutes here, because of wazero JIT under the race detector.
- `guard-data-access.sh` passes.
- `golangci-lint` reports 0 issues.

## Not verified

- The Pi itself: why `language-de` 1.1.24's live dir was missing there, and cleaning up any stale version records. That needs SSH and is split into a `blocked:env` card.
- No UI surface changed, so there is nothing to screenshot.

**Verdict:** safe to merge.
