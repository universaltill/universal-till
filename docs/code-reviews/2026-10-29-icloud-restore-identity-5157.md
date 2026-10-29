# Review: a restored install must not clone the till's cloud identity (ut-docs#5157)

**Date:** 2026-10-29 · **Branch:** `fix/5157-ios-restore-identity` · **Lane:** `lane:cloud-41`
**Author model:** Sonnet (Dev) from an Opus Architect brief · **Reviewer:** Fable (independent, different model; security card)

## What shipped

- `internal/enroll/restored.go`: `CheckRestoredInstall` runs once at boot in
  `internal/app/app.go`, after the settings store is built and before
  `settings.LoadRuntimeConfig` / `enroll.Init`. It compares the marker file
  `paths.Data("secrets", "install-id")` with the DB row
  `till_identity.install_id`. The iOS app excludes `secrets/` from the
  iCloud backup (#5140), so the marker only survives on the install that
  wrote it.
  - No DB row (fresh install, upgrade from before this change, a
    just-joined replica): adopt the marker and wipe nothing.
  - Row matches the file: no-op, no writes.
  - Row differs or the file is missing: reset the cloud identity with the
    existing `resetIdentityForPair` (the ADR-0116 D5 "wiped till" reset that
    pairing uses), set `till_identity.restored_at`, write a new marker, and
    write the DB row last.
  - Identity pinned in the environment, an unreadable marker, or a DB read
    error: no wipe.
- `resetIdentityForPair` also clears `till_identity.restored_at`, so the
  flag goes away once the till pairs again.
- A `restored` variant of the cloud-auth chip ("Restored on a new device —
  connect again"), shown while the till is unregistered and not vouched
  for by a main till.
- Plugin secret fields that can't be decrypted (the ADR-0082 key in
  `secrets/` did not come along) now say "enter it again"
  (`data.PluginSettingRow.Unreadable`).
- Help pages `claim.md` and `backups.md` in en/de/ar/fa/tr; 3 new keys in
  en/ar/fa/tr + `en.meta.json`.
- `scripts/ci/ios-secrets-backup_test.sh` pins the marker under `secrets/`,
  with a broken-fixture self-check.

No ADR: this reuses the ADR-0116 D5 reset and D6 chip, and ADR-0082 already
accepts plugin-secret re-entry after the key is lost.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | A crash after the reset but before the re-mark repeats the check on the next boot. The identity is empty by then, so `held` was false, `restored_at` stayed cleared, and the till ended up unregistered with no chip. | **Fixed:** `held` also counts an existing restored flag. Regression test `TestRestoreCheck_CrashBeforeRemarkKeepsRestoredFlag` failed before the fix ("restored flag lost…") and passes after it. |
| 2 | should-fix (scope) | The reset keeps other per-device identities that a clone shares too: `till_identity.cloud_id` (a once-promoted till's cloud id), and on a replica `sync.bearer`, `sync.till_id` and the LAN pins. | **Deferred** to ut-docs#5162 (same bug class, LAN plus cloud-id side). This card's scope is the marketplace device id and token. |
| 3 | nit | The new keys are not yet in the de/es/pt packs, so those locales fall back to English until the pack PRs land. | Pack PRs land in the same cycle (CLAUDE.md, lang-pack-drift). |
| 4 | nit | Help translations checked: meaning is right, the bold chip labels match each locale's key, and the structure guard is green. | No change. |

The reviewer found these correct:

- **No false resets.** An in-app restore keeps the live `till_identity.*` rows. Join snapshots strip `till_identity.*`, so a joined replica adopts the marker. Android doesn't back up its data. A self-update doesn't touch the data folder.
- **Boot ordering.** At the point of the call, `cfg.Marketplace` holds only the environment's values.
- **Replica.** It is re-vouched through the main till afterwards.
- **Chip gating.** The chip is a link, never a dialog, and never blocks checkout.
- **Marker write.** `MkdirAll` at 0700, temp file then rename, 0600, and the file is written before the DB row.

## TDD re-verification

The reviewer checked each fix by breaking it, running the tests, then restoring the code:

| Fix broken | Tests that failed |
|---|---|
| Restore branch disabled | 5 enroll tests |
| The `pair.go` clear removed | `TestPairClearsRestored` |
| `Unreadable` forced false | `TestListPluginSettings_UnreadableSealedValueIsFlagged` |
| Chip block disabled, or the view flag forced false | The chip test and the plugin-settings page test |

The orchestrator saw the new crash-window test fail before its fix.

## Verified beyond unit tests

- Guards: i18n, i18n-meta, help-drift, help-topics, help-breadcrumbs, data-access, rtl-logical-css, ci-tests-sandboxed, docs-shots (surface hash refreshed with `update-docs-shots-surface-hash.sh`), and `ios-secrets-backup_test.sh`.
- No existing screenshot changes. The new states show only after a restore or when a secret can't be read.
- **Not done:** no Xcode build or device run of iOS, since the cloud sandbox has none. No new Swift was needed: Go owns the marker inside the folder Swift already excludes.
- **Not looked at:** the chip and the plugin-settings note were not viewed in a browser (no screenshot). They reuse the existing chip partial and the field-hint markup.

## Verdict

Safe to merge.
