# Android manifest: stop CAMERA/RECORD_AUDIO/ACCESS_FINE_LOCATION implying required hardware (ut-docs#1770)

- **Date**: 2026-09-08
- **Branch / PR**: `fix/1770-android-manifest-implied-features`
- **Card**: ut-docs#1770 (`complexity:easy`, found by the independent review on ut-docs#1751)

## What shipped

`android/app/src/main/AndroidManifest.xml` declared `CAMERA`, `RECORD_AUDIO`
and `ACCESS_FINE_LOCATION` with no matching `<uses-feature required="false">`.
All three are on Android's "permissions that imply feature requirements"
list, so the Play Store **derives** the corresponding feature as
`required="true"` from the permission alone when the element is missing —
backwards from the stated intent ("a till may run on hardware without a
camera/microphone/location radio, so don't make the app store-uninstallable
there"). ut-docs#1751 already found and fixed the identical bug for the
Bluetooth permissions; this closes the same gap for the rest of the manifest.

- `android.permission.CAMERA` → `<uses-feature android.hardware.camera required="false">`
- `android.permission.RECORD_AUDIO` → `<uses-feature android.hardware.microphone required="false">`
- `android.permission.ACCESS_FINE_LOCATION` → **both**
  `<uses-feature android.hardware.location required="false">` and
  `<uses-feature android.hardware.location.gps required="false">` (this
  permission is declared only to unlock pre-API-31 Bluetooth scan results,
  not because the till has or needs a location radio — comment says so).
- Every manifest comment corrected to explain why the element is required,
  not why it was historically omitted (the omission was the bug).
- New `scripts/ci/guard-android-manifest-features.sh`: parses the manifest,
  cross-references declared `<uses-permission>` names against Android's
  implied-feature table, and fails if any implied feature lacks an explicit
  `required="false"` `<uses-feature>` element (present-but-defaulted-true and
  present-with-`required="true"` both fail, not just "element missing
  entirely"). Wired into `ci.yml` next to the other Android guards.
- `scripts/ci/guard-android-manifest-features_test.sh`: proves the guard
  rejects the original bug shape, the "element present but `required`
  omitted" variant, `required="true"` explicitly, and a partial fix covering
  only one of `ACCESS_FINE_LOCATION`'s two implied features — and accepts a
  permission with no implied feature (`INTERNET`) and the real, current
  manifest.

## Independent review

Fresh-context Sonnet subagent (this is a `complexity:easy` card — Sonnet
built it, so review relaxes to a different Sonnet instance rather than a
different model), isolated worktree, briefed to fact-check the Android
platform claims independently and to actually run things rather than just
read the diff.

**Verdict: SAFE TO MERGE, no blocker-class findings.**

What it verified beyond reading the diff:
- Independently fact-checked all three implied-feature claims (camera,
  microphone, both location features) against Android's own "permissions
  that imply feature requirements" table — all correct.
- Audited every `<uses-permission>` in the final manifest (13 total) and
  confirmed none of the implied-feature ones are left uncovered, and the
  rest (`INTERNET`, `FOREGROUND_SERVICE*`, `POST_NOTIFICATIONS`,
  `REQUEST_INSTALL_PACKAGES`, `BLUETOOTH_SCAN`/`BLUETOOTH_CONNECT`,
  `MODIFY_AUDIO_SETTINGS`) genuinely have no implied feature.
- **Independently reproduced the TDD claim rather than trusting the commit
  message**: extracted the actual pre-fix manifest from `main`'s tip
  (`6ce0a33`), ran the new guard against it directly, confirmed it failed
  with exactly the four expected errors (camera, microphone, location,
  location.gps); ran the guard against the fixed manifest, confirmed it
  passed.
- Ran the guard's own regression test suite (all 8 assertions pass).
- Validated the manifest XML and the `ci.yml` YAML both parse clean, and
  that the new CI steps are placed correctly (paired guard+regression-test,
  matching the existing convention for every other Android guard in that
  job).
- Ran `gofmt -l .` (clean) and `go build ./...` (clean) to confirm the
  Go side is untouched/unaffected.
- Ran the other Android guards (`guard-android-i18n.sh`,
  `guard-android-status-address.sh`, `guard-android-external-links.sh`)
  against the branch — all still pass, unaffected.
- Judged the `web/help/` manual-parity rule explicitly: **not applicable**
  — this is a Play Store metadata/installability fix with zero effect on
  anything a shop operator sees or does.

**Non-blocking nits, accepted as-is:**
- The manifest's pre-existing (from #1751, untouched here) comment claiming
  `BLUETOOTH_SCAN`/`BLUETOOTH_CONNECT` are on Android's implied-feature list
  is factually inaccurate per current platform docs — only legacy
  `BLUETOOTH`/`BLUETOOTH_ADMIN` actually are. Correctly out of scope for
  this card (a manifest *comment* accuracy issue, not a functional bug —
  the new guard's own `IMPLIED_FEATURES` map correctly excludes
  `BLUETOOTH_SCAN`/`BLUETOOTH_CONNECT`, so it doesn't demand a bogus
  declaration for them). Worth a small follow-up someday, not filed as its
  own card given how minor it is.
- The guard's `IMPLIED_FEATURES` map includes several permissions not
  currently declared anywhere in the manifest (telephony, wifi,
  `ACCESS_COARSE_LOCATION`) — deliberate, forward-looking coverage per its
  own comment, not scope creep.

## Verified beyond the automated tests

No physical-device verification needed or claimed: this change alters only
what the Play Store derives from the manifest at *submission* time — it has
no runtime effect on a sideloaded APK (every till running today), and the
existing runtime permission-request flow (`MainActivity`'s
`onPermissionRequest`, unchanged by this diff) is what actually gates
camera/microphone/location use on-device. Verification here is the guard
test suite (proves the check fires on the exact original bug and passes on
the fix) plus the independent review's own from-scratch reproduction of
that same claim against the real pre-fix commit.

## Gates run

`gofmt -l .`, `go build ./...`, XML well-formedness, YAML validity,
`guard-android-manifest-features.sh` (new), `guard-android-manifest-features_test.sh`
(new), `guard-android-i18n.sh`, `guard-android-status-address.sh`,
`guard-android-external-links.sh` + its own regression test,
`guard-data-access.sh`, `guard-kiosk-engine.sh`. All green. Full
`android-ci.yml` Gradle compile gate runs on push (no Android SDK available
in this session) — a manifest-only text change carries negligible risk of
breaking Kotlin compilation.
