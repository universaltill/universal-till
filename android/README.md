# Universal Till — Android

The Android till app (ADR-0023, `ut-docs`; spec `specs/013-mobile-shared-core`):
the exact same Go server as the desktop/CLI build (`internal/app.Run`,
via the `mobile` package), embedded in-process in a real Android app —
no rewrite, no second plugin host.

## Architecture

- `../mobile` (Go) is bound into `app/libs/unitill-mobile.aar` via
  `gomobile bind` — an Android library containing the Go server
  cross-compiled for all 4 ABIs, plus generated Java bindings
  (`mobile.Mobile.start/stop/isRunning`).
- `TillService` (a foreground `Service`) starts/stops the server and
  survives `MainActivity` being destroyed and recreated — a screen
  rotation does **not** restart the till (verified: same process, same
  port, same server instance across rotation).
- `MainActivity` binds to `TillService` and points a `WebView` at
  whichever loopback address it reports — the same "start, wait, then
  show a window" shape `cmd/unitill-desktop/desktop.go` uses, minus the
  child-process spawn (mobile apps can't do that).
- Cleartext HTTP is allowed **only** to `127.0.0.1`
  (`network_security_config.xml`) — the embedded server never binds
  anything else, and this keeps the app from accepting cleartext to
  anything else it might ever talk to.

## Prerequisites

- Android SDK + NDK (`brew install --cask android-commandlinetools`,
  then `sdkmanager` for a platform, build-tools, and an NDK version —
  see `ut-docs/adr/0023-android-ios-till-strategy.md` for the exact
  versions this was built/verified against).
- `gomobile`/`gobind` on `PATH` (`go install
  golang.org/x/mobile/cmd/gomobile@latest` — `golang.org/x/mobile` is
  already a tool dependency in `../go.mod`, so no extra `go get` is
  needed in this repo).
- `ANDROID_HOME`/`ANDROID_SDK_ROOT` set (persisted in `~/.zprofile` on
  the machine this was set up on).

## Build

```sh
./gradlew assembleDebug
```

The `generateAar` Gradle task runs `gomobile bind` automatically as
part of `preBuild` — `app/libs/unitill-mobile.aar` is a build artifact,
never committed (see `.gitignore`), always regenerated from Go source.
It's deliberately never treated as up-to-date/skippable: `internal/app
.Run` (what `./mobile` wraps) transitively imports far more of
`internal/*` than Gradle's file-based staleness tracking could express
correctly, so this always re-runs rather than risk silently packaging a
stale `.aar` (a real gap independent review caught and this fixes).

## Run on an emulator

```sh
# One-time: install the emulator + a system image, create an AVD
sdkmanager "emulator" "system-images;android-36;google_apis;arm64-v8a"   # arm64 host; use x86_64 on Intel Macs
avdmanager create avd -n unitill-test -k "system-images;android-36;google_apis;arm64-v8a" -d pixel_6

# Boot + install + launch
emulator -avd unitill-test &
adb install -r app/build/outputs/apk/debug/app-debug.apk
adb shell am start -n com.universaltill.pos/.MainActivity
```

## Verified working (2026-07-25/26)

Live-verified end-to-end against a real built APK on a real (emulated)
Android device, not just "it compiles" — including a full pass AFTER
independent review's 4 findings were fixed, not just before:

- Real APK installs and launches; the embedded Go server genuinely
  boots (`GoLog: Universal Till POS starting...` in `adb logcat`),
  binds a real loopback port, and the setup wizard's actual first
  screen (language picker, ar/en/fa/tr) renders correctly in the
  WebView.
- Real interactivity: tapping through the wizard (language → country
  step) works — a genuine touch → WebView → server round trip, not a
  static render.
- **The foreground-service fix genuinely matters**: verified via a real
  screen rotation that the server process/PID and listening port are
  identical before and after (no restart).
- **The `configChanges` fix genuinely matters too**: with it, a rotation
  produces no new `onCreate`/WebView-reload at all (confirmed via
  logcat — no second "listening on" line, meaning the Activity itself
  wasn't recreated, not just that the server survived); the layout
  correctly reflows to landscape via ordinary view resizing.
- **The `webView.destroy()` fix verified via a real exit path**:
  backing out of the Activity twice produces no crash (`adb logcat`
  clean of any `FATAL`/`AndroidRuntime` exception for this app), and
  `TillService` correctly keeps the process running afterward (by
  design — a real POS terminal isn't "done" just because its screen
  isn't shown).
- **`generateAar`'s `outputs.upToDateWhen { false }` fix verified**: the
  rebuild after applying all 4 fixes shows `> Task :app:generateAar`
  actually executing (not `UP-TO-DATE`), confirming it reruns every
  time as intended.
- `TillService`'s `android:exported="false"` verified for real, not just
  declared: `adb shell am stopservice` against it was rejected by the
  OS itself ("Permission Denial: ... not exported") — confirms external
  processes genuinely cannot reach it, exactly as intended.
- Real launcher icon (from `web/public/assets/logo/ut-logo.svg`) and a
  proper Android notification-permission flow (best-effort — the
  service runs regardless of whether it's granted).

## Not yet done

- Release signing/distribution (Play Store or sideload APK signing —
  needs Farshid's own signing keys/Play Console decisions, ADR-0023).
- Physical hardware integrations (printer/scanner/card reader) — the
  `runtime:"go"` process-spawning plugin type doesn't port to mobile at
  all (ADR-0023 §2); needs native adapters, not attempted here.
- A "stop the till" affordance in the UI itself (today the service just
  keeps running once started, matching a real POS terminal's actual
  requirement — but there's no in-app way to deliberately stop it yet
  short of force-stopping the app from Android's own app settings).
- CI build for this app (not wired into `.github/workflows` — the
  Android SDK/NDK footprint is large and this hasn't been scoped for
  CI yet).
