# Review: Android "Save to Downloads" fails on USB-C storage (ut-docs#5619)

- **Cause**: `POST /api/backup/save-copy/{name}` copies in the Go process to
  `os.UserHomeDir()/Downloads`. On Android that is `/sdcard/Downloads` (wrong
  name, and scoped storage forbids the app creating it); with storage on a
  USB-C drive nothing reachable exists. Every error collapsed to "Could not
  save the backup copy." with no reason.
- **Fix**: in the Android app the button calls `AndroidKiosk.saveBackupCopy`,
  which opens the system save picker (`CreateDocument`: Downloads, a USB
  drive, any provider) and streams the backup from the till's own loopback
  download into the chosen Uri. The page shows a translated result
  (`settings.backup.picker_*`). `POST /api/backup/copied/{name}` clears the
  copy-off reminder. The server save-copy path refuses on Android with its own
  reason and now names disk full / permission / unreachable / use-picker.
- **Tests**: `backup_save_error_test.go` (reason keys, Android refusal, page
  wiring), `backup_copy_chip_test.go` (picker copy records only real
  backups), `BackupSaveLogicTest` (name validation, URL, result codes),
  cashier/anonymous 403 row for the new route, demo-mode block.
- **Not verified**: no run on a real tablet or USB drive (picker, write
  to a USB provider, unplugged-mid-write). Only JVM unit tests, Go tests and
  a Kotlin compile.
- **Independent review (Opus subagent)**, fixed: (1) the picker's download GET
  cleared the reminder before the write succeeded -> `?via=picker` skips it,
  only `/copied` records; (2) script sat inside `{{ if .backups }}` in the
  refreshable region (never runs on a fresh till) -> moved outside; (3)
  duplicate listener guard; (4) connect/read timeouts and deletion of a
  partial document. Left: (5) Windows disk-full codes map to the generic
  message (desktop path, not this card); process death while the picker is
  open drops the result.
