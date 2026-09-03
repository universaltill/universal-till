# Android in-app update, PIN-gated (ut-docs#1246)

**Date:** 2026-09-03
**Card:** ut-docs#1246 (`source:user`, open since 2026-08-28)
**Devices:** TECLAST P50T (arm64, Android), real hardware throughout

## Problem

The Android till had **no in-app update path at all**. Settings and the status
chip both rendered *"In-app update isn't available for this install"*, so every
build meant telling the operator to download and reinstall an APK by hand. With
a pilot café about to start testing, that makes the feedback loop unworkable.

`selfupdate.Supported()` returns false on android by design and that is
**correct**: the Go core ships as a native library inside the APK, and only the
package installer may replace an app's own code. `Apply()` only knows how to
swap a `.tar.gz`, which is never published for android. No amount of work in
`internal/selfupdate` can fix this — the shell has to drive the installer.

## Three defects, each hiding the next

Found in order, on the device. Each one had to be fixed before the next became
visible, which is why the symptom looked like "nothing happens" throughout.

1. **No android branch existed.** Both `updateUnavailableHTML`
   (`internal/pages/update_api.go`) and the status chip
   (`web/ui/layouts/base.html`) fell through to the unix-kiosk dead-end text.
   Note these are *two* copies of the same decision, despite
   `DownloadLinkActionable`'s doc comment claiming it is "the single source of
   truth shared by" both — fixing only the first left the chip, which is what
   the operator actually looks at, still showing the dead end.

2. **`RECEIVER_NOT_EXPORTED` silently swallowed the completion signal.**
   `DownloadManager.ACTION_DOWNLOAD_COMPLETE` is broadcast by the *system*, so a
   not-exported receiver never fires. The 142MB APK downloaded correctly and
   then sat on disk forever. Replaced with an explicit poll of
   `DownloadManager.query()` rather than exporting the receiver — exporting it
   would let any app on the device spoof "your download finished", and polling
   has explicit terminal states and no registration to leak.

3. **Lock-task mode killed the installer.** Logcat:
   `Attempted Lock Task Mode violation r=…packageinstaller/…InstallStart`.
   Android refuses to start a non-allowlisted activity from a pinned app, with
   no dialog and no exception — invisible from inside the app. The shell now
   releases the pin before launching the installer, the same way
   `KioskBridge.exitLockdown` does for the manager's exit-to-OS path.

## The security problem that fix created, and the gate

Releasing the kiosk pin **is** the capability `POST /api/settings/exit-to-os`
exists to guard. The update chip lives in `base.html`, so it renders on *every*
page including the sale screen — an ungated Update button would have been a
one-tap kiosk escape for any cashier. That is worse than the bug being fixed.

So the bridge is now reachable from exactly one place:

- **status chip** (any page, any user) → a plain link to `/settings#android-update`
- **Settings status line** → the same anchor, not the bridge
- **`settings.html#android-update`** → a manager-PIN form, the only caller of
  `window.AndroidKiosk.installUpdate()`

`POST /api/update/android-install` mirrors `exit-to-os` exactly, including
rejecting a blank PIN **before** `AuthorizeManager` so it cannot burn the
device-wide failed-attempt budget that keypad login shares (5 failures = 30s
lockout). It fails closed when `AuthSvc` is nil.

`installUpdate()` deliberately **takes no argument**. The release APK URL is a
compile-time constant, so page content can never steer what gets installed —
the bridge is JS-reachable, and an argument would turn it into "install
arbitrary package". Authorisation returns nothing the page can install *with*,
so forging a success response grants no new capability. Android separately
enforces that the new APK carries the same signing key.

## Verified on real hardware

- Settings status line and chip both render the actionable control (previously
  the dead-end text) — confirmed by authenticated `curl` against the tablet and
  by screenshot.
- Tap → 142MB download lands in the app's external files dir → **package
  installer opens at 6 seconds**. Stopped there deliberately: the newest
  release is v0.9.3, a pre-schema-reset build that would destroy the tablet's
  database. The staged APK was deleted and the till left on its own build.
- `go build ./...`, `gofmt -l` clean, `go test ./...`, and the CI guards
  (`guard-i18n`, `guard-data-access`, `guard-kiosk-engine`,
  `guard-android-i18n`, `guard-htmx-loaded`) all pass.
- No new i18n keys: reuses `shifts.manager_pin`, `settings.update.download`,
  `status.update_available`, so no locale drift.

## Known limits, not addressed here

- **Unattended overnight update is impossible on this device.**
  `dumpsys device_policy` reports `Device Owner Type: -1` — the app is not
  device owner, so it uses ordinary screen pinning and cannot install silently.
  An overnight job could download but the installer would still prompt with
  nobody there. Real unattended install needs device-owner provisioning
  (factory reset, no accounts on device). This matters most for markets with no
  Play Store at all (Iran), where the APK path is the *only* path.
- **The APK is 142MB**, which is far outside the normal 10–50MB range.
  `lib/arm64-v8a` 60.7MB + `lib/armeabi-v7a` 56.7MB = 117MB of it. Two levers,
  neither taken here: gomobile's ldflags carry only the version stamp and **no
  `-s -w`** (the build file's own comment calls it "the ~90MB unstripped
  .aar"), and `armeabi-v7a` is dead weight for an arm64 fleet. Tracked
  separately.
