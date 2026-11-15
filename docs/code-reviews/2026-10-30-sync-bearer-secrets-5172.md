# Review: the LAN sync bearer moves out of the backed-up database (ut-docs#5172)

**Date:** 2026-10-30 · **Branch:** `fix/5172-sync-bearer-secrets` · **Lane:** `lane:cloud-41`
**Author model:** Sonnet (Dev), from an Opus Architect brief · **Reviewer:** Opus 5.5 (independent, a different model from the author)

## What shipped

- **`internal/data/settings_sync_bearer.go` (new):** `KeepSyncBearerOutOfDB(ctx, db, path)` registers a per-`*sql.DB` redirect of the `sync.bearer` key to a file.
  - `SettingsRepo` is built in ~50 places, so the redirect is keyed on the database, not on a repo instance. An unregistered database (tests, seed tools) keeps the old settings-row behaviour.
  - Migration: a non-empty row is written to the file first. The row wins over an existing file, because it is a pre-upgrade install, a fresh join's `ApplyReplicaIdentity` row or a restored pre-upgrade backup. Only then is the row deleted.
  - The delete runs on one pinned connection with `secure_delete` on, then `wal_checkpoint(TRUNCATE)`, so neither the live `.db` nor its `-wal` keeps the bytes (review F1).
  - Fails closed: on any error the row stays and nothing is registered. `app.go` logs and carries on, so sync never breaks.
  - File writes: `MkdirAll` 0700, temp file + rename, 0600. An empty value removes the file. Writes are serialised by `syncBearerMu`.
- **`internal/data/settings_repo.go`:** for a registered database, `Get`, `Set`, `SetTx`, `SetMany`, `GetOrCreate`, `Delete`, `GetByPrefix`, `All` and `ClearReplicaIdentity` treat `sync.bearer` as the file. The file write in `SetTx` and `SetMany` is not part of the transaction; the code says so.
- **`internal/app/app.go`:** registers `paths.Data("secrets", "sync-bearer")` right after `ApplyReplicaIdentity` and before the settings store and `CheckRestoredInstall`. iOS excludes `secrets/` from backup. Android has `allowBackup=false`.
- **`internal/enroll/restored.go`:** the restored-replica `held` check also counts `sync.till_id`. A restore onto another device no longer brings the bearer, and without this a never-cloud-registered restored replica lost #5162's restored flag (review F2).
- **`scripts/ci/ios-secrets-backup_test.sh`:** pins the bearer file under `secrets/`.
- **Docs:** ut-docs `architecture/local-backup.md` gains a "No LAN credential in a backup" section (branch `docs/5172-sync-bearer-secrets`). Nothing changes for the shop owner, so no help topic changes.

No ADR is needed: this follows the existing `secrets/` pattern (install marker, approval key, plugin-settings key). The card's option 2 (rotation) is split out to ut-docs#5198.

## Findings (Opus review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | medium | The migration's DELETE ran without `secure_delete`, so the bearer's bytes stayed in the live `.db` and `-wal`, which iCloud backs up. | **Fixed:** `secureDeleteBearerRow`. `TestSyncBearer_MigrationLeavesNoBytesInLiveFiles` reads the raw files and failed for both before the fix. Both the pragma and the checkpoint are needed. |
| F2 | medium | `held` keyed only on `sync.bearer`, which a restore no longer brings, so #5162's restored flag silently stopped being set for an unregistered replica (re-join then refused over unsent day closes). | **Fixed:** `held` also counts `sync.till_id`. `TestRestoreCheck_UnregisteredReplicaWithoutBearerFileSetsRestoredFlag` uses a real registered repo with no file; it failed before and passes after. |
| F3 | medium (scope) | Bearers copied before the upgrade stay valid: snapshots already in `backups/`, off-device copies, stale bytes from earlier joins. | **Deferred** to ut-docs#5198 (main-till bearer rotation). Acceptance criterion 1 holds for backups taken after the upgrade only. |
| F4 | low | `syncBearerMu` guarded `GetOrCreate` but not the other writers. | **Fixed:** every file write goes through `storeSyncBearer` (locked). |
| F5 | low | `ClearReplicaIdentity` removes the file after commit; a failed removal leaves a stale file. | **Accepted:** every reader is gated on `sync.primary_url`, which is cleared; the next join's row overwrites the file. |
| F6 | low | The new grep in `ios-secrets-backup_test.sh` has no broken fixture proving it can fail. | **Accepted:** single literal grep; covered by F-level review. |
| F7 | low | `TestSyncBearer_EmptySetAndDeleteRemoveFile` would pass vacuously if `Set` wrote the row. | **Accepted:** `TestSyncBearer_RegisteredSetWritesFileNotRow` catches that regression. |

Checked and sound: every redirect; no non-test code reads or writes `sync.bearer` through raw SQL except `ApplyReplicaIdentity`, which the migration picks up; boot order; restore flow (a post-upgrade backup on the same device keeps the device's file; on another device the till joins again); `paths.Data`, `MkdirAll`.

## Verified beyond the automated tests

- The reviewer reverted production code and watched tests fail: `Set` writing the row fails `TestSyncBearer_RegisteredSetWritesFileNotRow` and `TestSyncBearer_SnapshotCarriesNoBearer`. Dropping the file removal in `ClearReplicaIdentity` fails `TestSyncBearer_ClearReplicaIdentityRemovesFile`. Both of the reviewer's own tests failed before their fixes.
- No UI surface changed, so there was no visual check.

## Gate

`gofmt`, `go build ./...`, `go vet`, `go test ./...`, `golangci-lint run ./...`, `guard-data-access.sh`, `ios-secrets-backup_test.sh` and the `ci.yml` build-job guards. `guard-deadcode-baseline.sh` (the local tool is built with an older Go) and `guard-shellcheck-version.sh` (shellcheck not installed) could not run in this container; CI runs both.

**Verdict:** safe to merge.
