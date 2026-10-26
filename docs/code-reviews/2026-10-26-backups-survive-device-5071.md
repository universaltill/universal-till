# Review: backups survive the device — reminder chip and "Replacing a till" (ut-docs#5071)

- **Card**: ut-docs#5071, part of ut-docs#5058 (replace a dead main till).
- **Branch**: `fix/5071-backups-survive-device` (on top of #5070's branch).
- **Investigation**: ut-docs `reference/investigation-5058-backup-restore.md`
  §1.2 and recommendation 3.

## What shipped

- A manager status chip **No backup copied off this till in 7 days**
  (`GET /ui/backup-copy-chip`, `backup_copy_chip.html`), linking to
  Settings → Backups, with the why/how in its title. Never a modal.
  - Shown when neither a copy (download or Save to Downloads) nor the
    reminder's start is within 7 days. The reminder clock starts at the
    first check, so a newly set-up till is not nagged on day one (and
    docs-shots' fresh tills never show it).
  - `GET /api/backup/download/{name}` (not a HEAD probe) and
    `POST /api/backup/save-copy/{name}` record `backup.last_copied_at` on
    success.
  - A reminder start in the future (a clock that stepped back) or an
    unreadable one is reset to now; a copy dated in the future is ignored.
  - A restore drops both keys (`db.RestoreDropKeys`), so an old backup's
    dates never show the chip right after a restore.
  - Both keys are per-till (`data.PerTillSettingPrefixes`): each till's
    backups are on its own disk; the admin sync never carries them.
  - Gated by `data_management` (manager/admin); a cashier gets an empty
    200 (cashier-is-sale-only).
  - Space: the chip is not in the sale-screen `:has()` list that brings
    the status bar onto the sale screen; it sits in the existing bar. The
    poll is every 10 min, classified in `internal/auth` as a shell poller;
    demo mode allows the GET.
- Help `backups.md` (en/de/tr/ar/fa): a "Replacing a till" section — the
  cloud keeps no sales, receipts, customers or PINs on any plan; where the
  backups are per OS (macOS, Windows, Linux, `.deb`, Android/iOS); old
  till starts → Back up now → Save to Downloads → USB; disk readable →
  newest file in `backups`; new till → finish setup → Restore a backup;
  restore before the first sale (replace, not merge); the cut-off. The
  `.deb` path is `/opt/unitill/data/backups` (the systemd unit's
  `UT_DATA_DIR`); two stale code comments saying `/var/lib/unitill` were
  corrected too.
- New keys: `status.backup_copy.label`, `status.backup_copy.hint`
  (en/ar/fa/tr).
- The wording for the #5058 replace dialog is handed to the replace-flow
  builders (it lives in ut-my-shop and in UT-1's confirm panel, not in
  this repo).

## Tests (TDD: written first, failed to build/fail before the code)

- `internal/pages/backup_copy_chip_test.go`: a fresh till shows nothing
  and records the reminder start; after 8 days without a copy a manager
  sees the chip (link, testid, no modal) and a cashier does not; a real
  download clears it; an 8-day-old copy brings it back; the start is
  written once; a future start does not silence it (fails without the
  reset — checked); an anonymous poll writes and shows nothing; Save to
  Downloads records a copy, a HEAD probe does not.
- `internal/data`: both keys are per-till and in `db.RestoreDropKeys`.

## Looked at (Tester/UX)

Throwaway till (temp `UT_DATA_DIR`, port 8093, killed after) with the
reminder start set 25 days back, screenshots read: Settings home and the
sale screen at 1024×600 in en, tr and fa (RTL: chip at the inline start,
digits localised), and 360×740 en (phone: the chip on its own row above
the online light). The chip fits on one line in the 1024 bar in en/tr/fa;
the sale screen layout is unchanged (the bar was already there). Not
looked at: German (pack), dark theme for the chip (same amber pill and
colours as the existing disk-low chip), real touch hardware.

## Independent review (Sonnet, read-only; ran build, vet, pages/data/auth tests and six guards: all ok)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `.deb` backup path wrong in all five help files (`/var/lib/unitill`, from stale comments) | Fixed: `/opt/unitill/data/backups`; comments corrected; "by default" for Linux |
| 2 | major (judgement) | "Save to Downloads" on the till itself clears a chip that says "copied off this till" | Accepted, owner's wording kept: the till cannot see a USB copy; the hint says to keep the file on a USB stick or another computer, and the help says so. HEAD probes no longer count |
| 3 | minor | After a restore the old backup's reminder dates come back | Fixed: dropped at restore |
| 4 | minor | A future timestamp silences the chip for good | Fixed: reset to now (a clamp alone would not have helped) |
| 5 | minor | A GET writes a setting | Accepted: one write per till (managers only), documented in the code; each write bumps the admin generation once, the keys are per-till and skipped by the admin sync |
| 6 | minor | Test gaps | Fixed: write-once, future start, anonymous, save-copy, HEAD |
| 7 | nit | const block split the PerTillSettingPrefixes doc comment | Fixed |
| 8 | nit | Mixed apostrophes in Turkish | Fixed |

## Docs-shots

Not run (Linux reference fonts needed). No captured pixel changes: the
chip never renders on a fresh till (reminder starts at the first check,
with docs-shots' pinned clock), the mount is `:empty { display: none }`.
Surface hash refreshed with `write-manifest.js`
(`Docs-Shots-Unchanged: true`).

## Gate

`go build ./...`, `go vet ./internal/...`, full `go test ./...`, every
ci.yml guard (two environment-only exceptions as on #5069/#5070).

## Replace-dialog wording (for the #5058 replace flow; not in this repo)

- my. Replace dialog, after the "copies your shop" paragraph:
  "Sales, receipts, customers and staff PINs are not kept in the cloud on
  any plan. They come back only from a backup of {name}: on the new till,
  Settings → Backups → Restore from a file, before its first sale."
- Till confirm panel (UT-1), under the loss line: "To bring back sales,
  customers and staff PINs, restore a backup of {name} on this till after
  setup (Restore a backup on the welcome page)."

## Verdict

Safe to merge after #5070.
