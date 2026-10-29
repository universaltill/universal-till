# Review — iOS: exclude till/secrets from the iCloud backup (ut-docs#5140)

Date: 2026-10-29 · Branch: `fix/5140-ios-secrets-backup-exclude` · Lane: `lane:cloud-54`

## What shipped

`TillServer.dataDirectory()` keeps the till's data in Application Support,
which iOS backs up. An iCloud restore onto a second device could therefore
yield two devices sharing the directive key, device token and device_id (and,
with ut-docs#5127, the approval key).

- `TillServer.excludeSecretsFromBackup(in:)` creates `till/secrets` and `till/tls`
  (0700, as the Go side does) and sets `isExcludedFromBackup = true` on both. The flag covers
  everything inside, including files the Go server writes later. It runs on every
  launch from `dataDirectory()`, before `MobileStart`, so an existing install gets
  the flag on its next start and a restore or OS update that dropped it is repaired.
- `scripts/ci/ios-secrets-backup_test.sh` (wired into `ci.yml`) pins the wiring
  and fails against five broken fixtures (flag off, never applied, call removed,
  wide permissions, wrong folder name). The Swift compiles only on ios-ci's macOS
  runner, so a wiring test is the Linux-side check.
- `ios/README.md` data-directory row updated.

## Verification

- `bash scripts/ci/ios-secrets-backup_test.sh` — OK; neighbouring iOS wiring test and
  the sandbox/mail/core-neutral guards still pass.
- Not driven on a device or simulator: a cloud lane has no macOS. Whether the
  restored backup omits `secrets/` is the owner's check on a TestFlight build.

## Review

Independent Opus 5.5 pass, three findings:

1. **High — fixed (claims) / follow-up (gap).** The device id and token are
   `marketplace.device_id`/`marketplace.token` rows in SQLite (`internal/enroll/enroll.go:81-84`),
   not files in `secrets/`, so this change does not stop a restore cloning them. The
   LAN TLS key sits in `tls/`, which is now excluded too. Comments, guard header and
   README no longer claim the token is covered. The DB-held credential is a separate
   card (see the PR / issue comment).
2. **Medium — fixed.** `try excludeSecretsFromBackup` inside `dataDirectory()` would have
   blocked boot (and the ADR-0075 recovery screen) on a metadata error. It is now
   logged and ignored; the guard pins the `NSLog`.
3. **Low — noted.** `plugin_settings_key.bin` is excluded but the sealed plugin settings
   stay in the backed-up DB, so after an iCloud restore a till makes a new key and plugin
   secrets (PSP keys) must be re-entered. Same as the existing `VACUUM INTO` backup policy.
