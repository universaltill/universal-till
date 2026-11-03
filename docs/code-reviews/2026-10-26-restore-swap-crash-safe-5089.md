# Review: crash-safe backup-restore swap (ut-docs#5089)

## What shipped
- `restore-swap` journal written before the live DB is renamed to
  `pre-restore-<ts>.db`; cleared when the staged file is in place.
- `db.RecoverInterruptedRestore`: journal present + DB file missing → move the
  staged file in (old `-wal`/`-shm` removed first), else move the pre-restore
  copy back. Journal + DB present → journal dropped. Journal names are checked
  (base name, `pre-restore-` prefix, `.db`). Called first in
  `ApplyPendingRestore` and early in `app.go`, before the LAN-join pre-open.
- `app.go` skips `paths.MigrateLegacyData` and the join pre-open while a
  journal is pending, so nothing creates or copies in a DB file over the gap.
- `architecture/local-backup.md` (ut-docs) documents the swap and the
  #5069–#5071 restore behaviour.

## Review (Opus 5.5, independent; author Sonnet 5.5)
No blocking findings. Fixed: legacy-DB copy-in hiding the missing file (1),
stale WAL replay on finish (2), non-NotExist journal read errors swallowed (6),
undo error text names journal and expected copy (4), join pre-open guarded (5).
Accepted: directory fsync of journal/renames (3) — rename order across
directories on f2fs/ext4 relaxed mounts; follow-up if it shows up in the field.

## Verified
`go build`, `go vet`, `golangci-lint` (0 issues), `go test ./internal/db
./internal/app`; new tests cover finish, undo, journal-with-DB, traversal
refusal, clean swap leaves no journal, stale sidecars, `RestoreSwapPending`.

Safe to merge.
