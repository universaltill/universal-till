# Review: daily auto-backup Problems survive a restart + total failure is a Problem (ut-docs#3993)

Date: 2026-10-09 · Branch: `fix/3993-daily-backup-problems` · Built by Sonnet
(card `complexity:easy`), reviewed by an independent Opus 5.5 subagent in a
fresh worktree. Follow-up to ut-docs#3991
(`2026-10-09-daily-backup-no-photos-problem-3991.md`, findings 1 and 4).

## What shipped

- `db.BackupLacksPhotos(snapshot, assetsRoot)` (`internal/db/backup_assets.go`):
  opens a snapshot read-only (`mode=ro`) and reports whether it has no
  `ut_backup_assets` table while the shop has at least one photo
  `embedBackupAssets` would embed. The walk rules (scopes, symlinked scope
  root, tmp suffixes, 10 MB cap) moved into one helper,
  `walkBackupAssetFiles`, shared by both; embed behaviour is unchanged.
- `runDailyBackup(db, dbPath, assetsRoot)` (`internal/server/server.go`):
  - fresh-skip path (`checkFreshBackup`): resolves `backup.daily_failed`
    and re-derives `backup.daily_no_photos` from the newest backup, raised
    only if not already open (`raiseOnce` — the loop runs hourly), or
    resolved when that backup has its photos (or the shop has none);
  - total snapshot failure raises the new keyed Problem
    `backup.daily_failed` (one open entry), resolved by any written
    snapshot or by a fresh backup (e.g. "Back up now").
- `ut-docs/architecture/local-backup.md`: "Problems" paragraph.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor (near major) | An unreadable photo tree (EACCES/EIO/AV lock) — the likeliest cause of a photo-less backup — made `BackupLacksPhotos` return `(false, err)`, so the Problem was never re-raised after a restart, and a log line printed every hour. | **Fixed**: with the table absent, a walk error returns `(true, err)` and the Problem is raised with the cause. Test `TestBackupLacksPhotos_UnreadableTreeCountsAsLacking` (skips as root; CI runs as a normal user) — verified failing without the fix (`lacks=false err=…permission denied`) and passing with it, as a non-root user. |
| 2 | minor | A newest snapshot with a hot journal (power cut mid-embed) can't be opened `mode=ro` (`attempt to write a readonly database`) → an hourly log line, no answer. | **Fixed (log)**: a "can't tell" cause is logged once per distinct message, not hourly; nothing is raised or resolved on no answer. |
| 3 | minor | "Back up now" racing the hourly check between `VACUUM INTO` and the photo-table commit could raise a false no-photos Problem. | Accepted: seconds-wide window, the next hourly check resolves it. |
| 4 | nit | A manual backup (or a pre-#2724 one) that is the newest and lacks photos is reported under the "daily" key/wording. | Accepted: the fact (newest backup has no photos) is true and is what the shop needs to know. |
| 5 | nit | `backup.daily_failed` carries the raw error, which can include the data path. | Accepted: cause first, the logger redacts, the heartbeat cuts at 200 chars. |
| 6 | nit | `raiseOnce` only sees the 50-entry ring; an evicted entry is raised again (once per hour at most). | Accepted, arguably desired. |

Checked and fine: the read-only open leaves no `-wal`/`-shm` and the bytes
and mtime are unchanged (snapshots are rollback-journal); URI escaping with
`?`, `#` and `%`; the hourly cost (one read-only open, one `sqlite_master`
query, a walk that stops at the first file); SQL stays in `internal/db`;
`paths.Data` is used at the call site; nothing runs on the selling path.

## TDD re-verified

The reviewer stubbed the production code in its own worktree:
`TestRunDailyBackup_SnapshotFailureLogged`,
`TestRunDailyBackup_FreshNoPhotosReRaisedAfterRestart`,
`TestRunDailyBackup_TotalFailureRaisesThenResolves` and
`TestBackupLacksPhotos` all fail, and all pass on the restored tree.

## Gate

`gofmt -l .` is clean and `go build ./...` and `go vet ./internal/...` pass.
`go test ./internal/server/ -race` passes, and so do the backup and
snapshot tests in `internal/db`. Every `scripts/ci/guard-*.sh` in the
`build` job passes except three that this container can't run:
deadcode/golangci-lint (the local tool was built with an older Go than
1.27.1) and shellcheck (no binary installed). No shell scripts changed;
CI runs all three. Backend only: there is no UI surface (the Problems
panel and heartbeat already render keyed Problems), so no help topic
changes.

## Verdict

Safe to merge.

## CI follow-up (first push)

`build` failed: `TestRunDailyBackup_FreshNoPhotosReRaisedAfterRestart`. The
Problem message read `unitill-pos-[REDACTED].db`. The logger redacts
every message, and about 10% of `unitill-pos-YYYYMMDD-HHMMSS.db` names
look like a card number to it. Brute-forcing three days of per-second
names gave 25,920 hits. So the "names the file" assertion depended on the
clock. #3991's `TestRunDailyBackup_NoPhotosRaisesThenResolvesProblem`
had the same latent flake. Both now compare against
`logging.Redact(name)`. The product-side over-match is filed as its own card.
