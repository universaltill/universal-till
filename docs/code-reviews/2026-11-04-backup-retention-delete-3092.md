# Review: backup retention by days + Delete button (ut-docs#3092, reopened)

Builder: Sonnet 5.5. Independent reviewer: Opus 5.5 (read-only, ran build, vet and tests).

## Why the card was reopened

The first pass (universal-till#1500) shipped "keep the newest 14 files". The
owner asked for "keep the last 7 days, deleted when a new backup is made",
and the backups list had no way to delete one. The card's AC were a count, so
the 14 tablet backups were "correct" and the owner's rule was never built.

## What shipped

- `db.PruneBackupsOlderThan` keeps the last N days of `unitill-pos-*`
  snapshots and always the till's newest own snapshot. It skips uploaded
  files and `pre-restore-*` copies (those stay with `PrunePreRestore` and
  its ADR-0040 statutory floor, #3365) and any name the caller passes (the
  restore source just staged).
- It runs after every new backup (daily, Back up now, restore) and in
  housekeeping. The old 14-file cap is gone (`DefaultBackupKeep` deleted);
  `CapBackups` (25 % of free space) and the low-disk minimum still bound
  growth.
- Setting `backup.retention_days` (per till, default 7, 1-365), field on
  Settings → Backups; `POST /api/backup/retention`.
- `POST /api/backup/delete` and a Delete button with a confirm on every row
  except the till's newest own snapshot. `deny()` (`data_management`) on the
  server, so a cashier is refused even with a forged request; audit rows
  `backup_deleted`, `backup_retention_changed`.
- Help topic, `architecture/local-backup.md` (ut-docs), English keys + meta.

## Findings (Opus review) and what was done

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | `TestRunHousekeeping_LowDiskRunsAtOnceWithTheMinimums` seeded 0-9 day snapshots; the 7-day rule broke it | fixed (12 h spacing) |
| 2 | blocker | my new daily-backup test was flaky (names from two clock reads) | fixed (one `base` time), run `-count=3` |
| 3 | major | a failed settings read fell back to 7 days = pruning a 365-day history on SQLITE_BUSY | fixed: `BackupRetentionDaysChecked` returns an error; every deleting caller skips the prune on error. Test added |
| 4 | major | an uploaded backup (mtime = now) became "newest", so the real newest could be deleted and the upload could not | fixed: `NewestOwnBackup` used by prune, delete and the UI. Tests added |
| 5 | major | saving a smaller number pruned at once with no confirm | fixed: save never deletes; takes effect at the next backup. Test added; hint and help say so |
| 6 | minor | unstable sort / concurrent deletes could both pass the newest check | fixed: name tie-break + `backupMu` around list-and-remove |
| 7 | minor | a future mtime stays "newest" forever (also stops the daily backup) | pre-existing; **Backlog card** |
| 8 | minor | delete uses `deny()` not step-up | accepted: matches the card ("admin/manager or granted role"), the newest is protected, same gate as download and save-copy |
| 9 | minor | prune after staging could delete the restore source | fixed (`keepNames`) |
| 10 | minor | dead `DefaultBackupKeep` | deleted |
| 11 | minor | action cell could overflow in long locales | `flex-wrap: wrap` |
| 12 | minor | retention field hidden on a till with no backups | moved out of the list branch |
| 13 | nit | dead assignment on the low-disk path | restructured |
| 14 | nit | "Saved" message cleared by region refresh | retention form no longer refreshes the region |
| 15 | nit | a restore brings back that backup's retention number | one sentence in the help topic |

Not in this change: the confirm uses htmx `hx-confirm` (the browser's own
dialog, as the catalog and modifiers pages do). A till-styled confirm is a
separate UX card.

## Verified beyond unit tests

Tester drove the real app on the TECLAST tablet (Android, 1280×800,
release-signed 0.35.4-dev3092 built from this branch, installed over the
0.35.3 data):

- Before: 14 backups (21 Oct to 4 Nov). About 2 minutes after the app
  started, the first housekeeping sweep pruned to **6** (everything from
  30 Oct on); expected: only files within 7 days, the newest kept. Matched.
- Delete via the real touch UI: Delete → the confirm ("Delete this backup? It
  cannot be brought back.") → OK → "✓ Backup deleted", the list refreshed,
  file gone. Newest row has no Delete button and says "Newest" (first
  build had the label in the action cell and wrapped, fixed and re-checked).
- Server: newest → 409, `../x.db` → 400, no session → 401, retention 0 → 400.
- Back up now on the device created a new file and kept the in-window ones.
- The tablet ran the build from before the review fixes (findings 3-6, 9,
  12-14 landed after it); those are covered by the Go tests above, not
  re-driven on the device.
- Not checked: a cashier session on the tablet (cashier is refused by
  handler tests only); 360 px phone layout; German/Persian rendering (English
  keys only, Weblate translates); a screen reader.

Safe to merge: yes, once CI is green.
