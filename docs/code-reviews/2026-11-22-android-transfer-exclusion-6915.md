# Review — Android: keep secrets/ and tls/ out of backup and phone-to-phone transfer (ut-docs#6915)

Date: 2026-11-22 · Lane: lane:cloud-41 · Branch: `fix/6915-android-transfer-exclusion`
Author model: Sonnet (Dev) · Reviewer: Opus 5.5, fresh context, own worktree · Tester: separate agent

## What shipped
- `android/app/src/main/res/xml/data_extraction_rules.xml` (Android 12+): `<cloud-backup>` and
  `<device-transfer>` both exclude `domain="file"` `secrets` and `tls`. These are the folders under `filesDir`,
  the data root that `TillService.kt` passes to `Mobile.start`.
- `backup_rules.xml` (`<full-backup-content>`, API 24–30) has the same excludes.
- The manifest keeps `android:allowBackup="false"` and adds `android:dataExtractionRules` and `android:fullBackupContent`.
  On targetSdk 36, `allowBackup=false` does not stop phone-to-phone transfer. `<device-transfer>` is the
  part that fixes the bug.
- Excluding `secrets/` drops `secrets/install-id`, so a copied database is detected as a restored install by the
  existing `internal/enroll/restored.go` logic (as on iOS, #5140). No Go change.
- New CI guard `scripts/ci/guard-android-backup-rules.sh` and a sandboxed `_test.sh` (20 assertions), both wired into
  the `build` job of `ci.yml`. The guard checks:
  - the manifest attributes, and `allowBackup` still false;
  - both files, with their root elements;
  - both sections exclude secrets and tls;
  - no `<include>` elements;
  - `installMarkerRel` is still under `secrets/`;
  - the data root is still `filesDir`.
- Help: `web/help/en/backups.md` and `multitill.md` now cover a phone-to-phone transfer that brings the till's data.
  `android/README.md` has one line about this.
- Kotlin comments in `TapAndroid.kt` and `ReaderStore.kt` no longer say that `allowBackup=false` keeps the data off
  other devices.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | SC1091 in `guard-android-backup-rules_test.sh` (missing `# shellcheck source=`) | fixed |
| 2 | minor | The guard did not pin the data root (`Mobile.start(filesDir…)`). A move to `getDir()` would make the rules silently match nothing. | fixed, with a fixture |
| 3 | minor | The comments in `TapAndroid.kt:60` and `ReaderStore.kt:24` gave `allowBackup=false` as the reason the data is never copied | fixed (comments only) |
| 4 | minor | `keys/offsite-backup.key` and `fiscal/signing_device_credential.json` still travel on both platforms | deferred → ut-docs#6918 |
| 5 | nit | Root element names were not checked | fixed, with fixtures |
| 6 | nit | The help prose claimed every Android transfer brings the data. It now says "if the transfer brings the till's data along". | fixed |
| 7 | nit | `tools:targetApi="31"` on `dataExtractionRules` | accepted as is. There is no `lint {}` block and CI never runs `gradlew lint`, so `UnusedAttribute` on minSdk 24 is only a warning. |

## Verification
- TDD re-verified by the reviewer in its own worktree. With the manifest reverted and both XMLs deleted, the guard
  fails with 4 errors. Restored, it passes.
- The Tester mutated a copy of the tree, never the real one. The guard rejected each of these:
  - tls dropped from device-transfer only;
  - `allowBackup="true"` or the attribute missing;
  - `path="secret"`;
  - only `install-id` excluded;
  - an `<include>` added;
  - `domain="database"`;
  - a wrong rules reference.
- The orchestrator re-ran the following after the fix round, all green:
  - `shellcheck scripts/ci/*.sh` (0 issues);
  - the guard and its test (20/20);
  - `guard-ci-tests-sandboxed.sh`, `guard-fixed-dates.sh`;
  - the help guards (topics, terms, drift), `guard-compliance-claims.sh`.
- **Not run here:** there is no Android SDK, so no Gradle build, aapt2 link or merged-manifest check. The PR's
  `android-ci` builds the APK. A real phone-to-phone transfer is also not run → ut-docs#6917 (`blocked:env`).

Pre-flight: web/help topic updated (backups, multitill; no screenshot change, prose only); README.md n/a (no
platform claim changed; android/README.md updated); reference/ n/a (no ut-docs reference covers backup
exclusions); en.json n/a (no keys); ut-website n/a (no feature claim); follow-ups deferred → ut-docs#6917,
ut-docs#6918.

## Verdict
Approve. No blockers or majors remain.
