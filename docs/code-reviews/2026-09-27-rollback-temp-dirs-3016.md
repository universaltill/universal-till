# Review: plugin rollback ignores crash-leftover temp dirs and restores only after validation (ut-docs#3016)

- **Date:** 2026-09-27
- **Branch:** `fix/3016-rollback-temp-dirs`
- **Files:** `internal/plugins/rollback.go`, `internal/plugins/rollback_test.go`
- **Author model:** Sonnet (card is `complexity:easy`). **Reviewer:** Opus 5.5, fresh context, in a separate worktree.

## What shipped

1. **Crash leftovers are no longer treated as versions.**
   - `GetVersionHistory` and `cleanupOldVersions` now skip dot-prefixed entries (`isTempVersionDirName`). These are the `.store-*` dirs that `StoreVersion` creates with `os.MkdirTemp`. A version name can never start with a dot, because `pluginVersionPattern` rejects it.
   - `cleanupOldVersions` also drops stray files before the `> maxVersions` check. A leftover can therefore no longer take one of the 3 kept slots or evict a real snapshot.
   - `cleanupOldVersions` also sweeps stale leftovers, best-effort: `.store-*` under `versions/` and `.restore-*` under `<id>/`. It only removes those older than `staleTempDirAge` (1h), so a store or restore that is still running is never removed.
2. **The live-dir restore now runs after validation.**
   - `Rollback` still checks up front whether `<id>/<target>` is missing (`needRestore`).
   - The `restoreLiveDirFromSnapshot` call has moved to after `ParseManifest`, every `validate*` check and the DB writes, immediately before `tx.Commit()`. A refused rollback therefore leaves no orphan live dir.
   - If the restore fails, the deferred `tx.Rollback()` undoes the DB writes.
   - If the commit fails, the dir that was just restored is removed.

## TDD: each test was re-verified against the pre-fix `rollback.go` by the reviewer

| Test | Old code | Fixed |
|---|---|---|
| `TestGetVersionHistory_SkipsTempDirs` | FAIL: `history len = 2, want 1` | PASS |
| `TestStoreVersion_TempLeftoverNotCountedTowardMaxVersions` | FAIL: `real snapshot wrongly evicted` | PASS |
| `TestCleanupOldVersions_SweepsStaleTempDirsButKeepsFreshOnes` | FAIL: `stale .store- dir must be swept` | PASS |
| `TestRollback_RefusedValidationDoesNotLeaveOrphanedLiveDir` | FAIL: `live target dir must not exist after a refused rollback` | PASS |

The existing test `TestRollback_RestoresMissingLiveDirFromSnapshot` still passes, so the happy-path restore works at its new position.

## Findings

| # | Severity | Finding | Disposition |
|---|---|---|---|
| F1 | major (pre-existing) | `Rollback` calls `StoreVersion(current)` before it reads the target manifest. The `cleanupOldVersions` that follows can evict the target when it is the oldest of the 3 kept snapshots. The reviewer reproduced this with a probe test. | Out of this card's scope. Filed as **ut-docs#3032**. |
| F2 | minor | The restore copy now runs while the SQLite write transaction is open, so the lock is held for the whole file copy. | Accepted. Rollback is a rare operator action, other writers wait on `busy_timeout`, and checkout never depends on it. An alternative is to restore to `.restore-*` before `BeginTx` and do only the rename before commit. |
| F3 | minor | No test covers the "commit fails, so remove the restored dir" path or a failed restore. There is a residual race with a concurrent install creating an empty `<id>/<target>`. | Accepted. The path is sound by construction: `needRestore` is set only when the dir was absent, and a Rename onto a non-empty dir fails. The race needs a concurrent install of the same version and is not realistic. |
| F4 | minor | The orphan test only exercises a refusal from `ParseManifest`. It does not cover a `validate*` refusal inside the transaction. | Accepted. Every refusal returns before the single restore call, so both kinds of refusal take the same code path. |
| F5 | nit | `realEntries` still counted non-directory files toward the threshold. | **Fixed.** Only directories are counted now. |
| F6 | nit | The sweep's age gate uses the temp dir's own ModTime, which does not change while a deep copy is working inside a subdirectory. | Accepted. A copy would have to run for more than 1h. |

## Checks with no issue

- The sweep only touches direct children of `versions/` and `<id>/`, with exact prefixes and a validated plugin id. `DirEntry.IsDir()` is based on lstat, so symlinks are skipped.
- Every write goes through `MkdirAll`, there are no cwd-relative paths, and there is no raw SQL outside `internal/data`.

## Gate (local)

- `gofmt -l .`: clean. `go build ./...`: OK. `go vet ./internal/plugins/`: clean. `golangci-lint run ./...`: 0 issues.
- `go test -timeout 20m ./internal/plugins`: ok (264s). This is how CI runs this package.
- Rollback subset under `-race`: ok. The reviewer's full-package `-race` run was stopped after 285 passing tests, with 0 FAIL and 0 DATA RACE.
- `guard-data-access`, `guard-core-neutral`, `guard-i18n`, `guard-kiosk-engine`, `guard-pipefail-grep-q`: ok.
- `guard-deadcode-baseline` fails locally in the same way on `main` (`internal/logging/file.go` `Stderr`/`timestampWriter.Write`). The cause is that `./cmd/unitill-desktop` is skipped here because the GTK headers are missing. CI analyzes that root, so this is environmental and not caused by this change.

No UI, locale or help change, and nothing a shop owner can see beyond the version list no longer showing a bogus entry.

**Verdict: safe to merge.**
