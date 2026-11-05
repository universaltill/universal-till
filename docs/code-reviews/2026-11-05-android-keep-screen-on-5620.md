# Android till: keep the screen on (ut-docs#5620)

## Cause
`MainActivity` never set `FLAG_KEEP_SCREEN_ON` (no WakeLock or keepScreenOn
anywhere in `android/` or `web/`), so the till followed the OS display timeout
in every mode, kiosk and self-order included. Read-only check on the owner's
P50T tablet (app 0.36.0): `screen_off_timeout` = 15000 ms.

## Shipped
- `ScreenAwakeLogic` (pure, JVM-tested): 0/15/30/60/120/240/480 minutes, default 120, 0 = never.
- `MainActivity`: flag held from `onResume` to `onPause`; dropped after the idle window (monotonic clock); touch or key press re-arms.
- Bridge `AndroidKiosk.get/setScreenAwakeMinutes` (per device, SharedPreferences), UI in Settings -> Display (`web/public/screen-awake.js`, hidden without the bridge); English keys + meta; help topic `display.md`; `android/README.md`.
- Tests: `ScreenAwakeLogicTest` (5), `internal/pages/android_keep_screen_on_test.go`.

## Review (independent Opus pass)
No critical/high. Fixed: key press did not re-arm a lapsed flag; "4b." help item
did not render (now a numbered item); minute labels for 2/4/8 h now hours; wrong
comment. Accepted: bridge setter is not role-gated (same trust model as
`exitLockdown`/`installUpdate`; effect limited to battery/burn-in, value clamped);
"saved" feedback is a glyph. Deferred: self-order kiosk with a finite timeout
sleeps and a customer cannot wake it; help says to choose Never there. A
follow-up could default Never for the self-order profile.

## Device check (P50T, adb, app 0.36.0)
Bug reproduced read-only: the till's MainActivity window had no KEEP_SCREEN_ON
(`dumpsys window`), `screen_off_timeout` = 15000 ms, and with the till in front
`mWakefulness` went Awake -> Dozing within about 16 s of no touch.
The fix was NOT proven on the device: `adb install -r` of the debug build was
refused (INSTALL_FAILED_VERSION_DOWNGRADE, versionCode 10 vs 360002; the
installed app is release-signed), and forcing it would mean an uninstall and
loss of the tablet's data. Needs a release-signed build (next release) and a
re-run of the same dumpsys check.

## Not verified
Unit tests and compile only;
no screenshot of the new row; first red run of the Kotlin test was skipped
(logic and test written together).

## Docs screenshots
`web/help/img/manifest.json` re-pinned (surface hash + display topic hash) after
the `en/display` topic and settings template changed. `make docs-shots` run in
the linux/amd64 Ubuntu 24.04 container with the pinned fonts: every PNG came out
byte-identical to main (no pixel change; the new row is Android-only and not on
the captured screen), so only the manifest is committed. `claim.png` and
`notices.png` differ under emulation and were discarded, as in #2386.
`guard-docs-shots.sh` passes.
