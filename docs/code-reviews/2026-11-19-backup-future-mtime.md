# Review: a backup with a future file time no longer counts as newest forever (ut-docs#5523)

Date: 2026-11-19. Branch `fix/5523-backup-future-mtime`. Author: Sonnet (Dev). Reviews: Opus 5.5, two independent rounds (2026-11-12, 2026-11-19).

## What shipped
- `internal/db/backup.go`: backups are aged by an *effective time*. The mtime is trusted when it is at most 5 minutes past now. A later mtime falls back to the UTC stamp in the file name, which also parses for uploaded `-file` names. If that is in the future too, the age is *unknown*. An unknown-age file is never newest-by-time, never fresh, never age-pruned, and it sorts last. Seams: `ListBackupsAt(now)` and `runDailyBackupAt(now)`.
- `PruneBackups(keep)` (housekeeping, low-disk cap) holds `backupMu` and always keeps `NewestOwnBackup`.
- Settings → Backups shows the effective date.
- `internal/offsitebackup/uploader.go` (merged from main after the branch was cut): it judges ages with `ListBackupsAt(now)` on its injected clock. It uploads only own snapshots of known age and sends `EffectiveTime` as `taken_at`.

## Findings
| id | sev | finding | outcome |
|---|---|---|---|
| R1-M1 | major | `PruneBackups` could delete the only own backup when uploads outranked it | fixed + tests (keep=1, keep=3) |
| R1-L1 | minor | `PruneBackups` did not take `backupMu` | fixed |
| R1-L2 | minor | the stamp in an uploaded `-file` name never parsed | fixed |
| R1-L3 | nit | a redundant `AgeKnown` guard in `runDailyBackupAt` | kept on purpose |
| R2-B1 | blocker | after the merge with main, 7 `offsitebackup` tests went red. Their fixtures planted the "newer" snapshot with a future mtime and a future name, which is now an unknown age. | fixed: fixtures set mtimes from the test clock `e.now`; each test's intent is unchanged |
| R2-M1 | minor | the uploader sent the raw mtime as `taken_at`. ut-cloud refuses a value more than 10 min in the future, so a future-mtime snapshot could never upload. It also picked an unknown-age snapshot. | fixed + `TestUploadSendsNameStampWhenMtimeIsInTheFuture`, `TestUploadSkipsOwnSnapshotOfUnknownAge` |
| R2-N1 | nit | the uploader listed backups on the real clock, not its injected one | fixed |

Checked by R2 with no defect found: the `-file` slice is never at index -1 (guarded by `IsUploadedBackupName`); `PruneBackups` edge cases (all uploads, an unknown-age own file only, the protected file already in the head); lock ordering (no caller holds `backupMu`, so no deadlock); `PrunePreRestore` and `backup_import` are unaffected.

## Verification
- Mutations (R2, reviewer worktree):
  - `effectiveTime` → `(mtime, true)`: 6 db tests fail.
  - The `protect` keep removed: 2 tests fail.
  - Only the `n--` removed: the same 2 fail.
- Orchestrator re-check: with `uploader.go` reverted, both new uploader tests fail with the claimed messages; restored, the whole package passes.
- `go build ./...`, vet and gofmt are clean. `go test -count=1` passes for db, housekeeping, server, offsitebackup and pages (`-run Backup`). `guard-fixed-dates.sh`, `guard-data-access.sh` and golangci-lint (touched packages, 0 issues) pass.
- No visual check: only the date value in Settings → Backups changed.

Pre-flight: help topic `web/help/en/backups.md` n/a (no user-visible wording changed; its "newest backup is never deleted" stays true); `reference/` updated (ut-docs `architecture/local-backup.md`, `reference/design-5066-offsite-backup.md` row 1, ut-docs PR on `docs/5523-backup-effective-age`); README n/a; i18n n/a (no new keys).

Verdict: ship.
