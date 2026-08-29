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
- Real launcher icon (from `web/public/assets/logo/unitill-logo.svg`) and a
  proper Android notification-permission flow (best-effort — the
  service runs regardless of whether it's granted).
  The `mipmap-*/ic_launcher*.png` files are **generated, not authored** —
  regenerate them with `./android/generate-launcher-icons.sh` whenever the
  canonical mark changes, rather than editing the rasters by hand.

## Signing & release (done 2026-07-26)

Self-signed APK, direct sideload distribution — no Google Play involvement
(Farshid's choice, matching this app's "no Google contact" posture
everywhere else). `.github/workflows/release.yml`'s `android-app` job
builds, signs, and attaches `unitill-pos_<version>_android.apk` to every
GitHub release automatically, alongside the desktop builds; the site's
`/download` page picks it up with zero code changes needed (same live
GitHub Releases API mechanism the other platforms already use).

- Signing key: a 4096-bit RSA key, self-signed, `keytool`-generated,
  valid until 2053. **Two durable copies exist** — Azure Key Vault
  (`kv-unitill-dev`, secrets `android-release-keystore-base64` /
  `android-release-keystore-password` / `android-release-keystore-alias`)
  and this repo's GitHub Actions secrets (`ANDROID_KEYSTORE_BASE64` /
  `ANDROID_KEYSTORE_PASSWORD` / `ANDROID_KEY_ALIAS`, the ones CI actually
  reads). **Losing this key is effectively unrecoverable**: Android
  refuses to install an update signed with a different key over an
  existing install, so every future release must be signed with this
  exact key or every user who installed a prior release would need to
  uninstall and reinstall from scratch. Deliberately NOT Terraform-managed
  (a keystore isn't something `random_password` can generate, and
  `unitill-infra`'s apply flow is manual/gated) — the Key Vault copy is a
  plain secret, set directly, for disaster recovery only.
- `android/app/build.gradle.kts`: a release build stays **unsigned**
  (not silently debug-signed) unless `ANDROID_KEYSTORE_PATH`/
  `ANDROID_KEYSTORE_PASSWORD`/`ANDROID_KEY_ALIAS` are all set — loud
  failure (can't install) over a quiet trust gap. `versionCode`/
  `versionName` are overridable via `-P` Gradle properties; CI derives
  `versionCode` deterministically from the release's semver tag
  (`MAJOR*10000 + MINOR*100 + PATCH`) so it always strictly increases,
  which Android's package manager requires to treat a new APK as an
  upgrade (true for sideloading too, not just Play).
- Live-verified: a real signed release APK installed and launched on the
  emulator, and upgrading an existing install with a newer signed build
  (same key) succeeded with no uninstall needed.

## Kiosk lock-down (ut-docs#1254)

What's implemented now, all in `MainActivity`:

- **Screen-pinning Lock Task** (`startLockTask()`) engages automatically
  on launch and is re-asserted on every `onResume` (defense in depth —
  both calls are idempotent). Home and Recents are blocked while pinned.
- **Immersive full-screen** (androidx `WindowInsetsControllerCompat`)
  hides the status and navigation bars as a cosmetic second layer; a
  swipe reveals them transiently and they auto-hide again.
- **Navigation is confined to the till's own loopback origin**
  (`WebViewClient.shouldOverrideUrlLoading`) — a link this app doesn't
  control the far end of can't navigate this WebView off-origin. This
  isn't only kiosk hygiene: the exit bridge below (`addJavascriptInterface`)
  is only safe to expose because of it.
- **Exit = the server's own existing "exit to OS" escape hatch, no
  native-side auth.** `/api/settings/exit-to-os`
  (`internal/pages/settings_page.go`, ut-docs#1099) already does a live
  manager-PIN check server-side, and is reachable from both `/settings`
  (a signed-in operator) and `/login` (a fully anonymous, un-signed-in
  kiosk screen — the self-order case). It's issued as a plain `fetch()`
  from the page's own JS, not a top-level navigation, so it isn't
  observable via WebView navigation at all — `login.html`/`settings.html`
  call `window.AndroidKiosk.exitLockdown()` directly on a successful
  response instead (`MainActivity.KioskBridge`, exposed via
  `addJavascriptInterface` — safe only because `shouldOverrideUrlLoading`
  confines this WebView's navigation to the till's own loopback host; an
  earlier draft of this card claimed safety from the WebView "only ever"
  loading loopback content, which was false — an ungated in-page link
  (`my_reports.html`'s GitHub issue link) could otherwise navigate this
  same WebView, and the bridge, to an untrusted origin). Re-locking is
  defensive on two fronts: `onPageFinished` re-engages
  the moment navigation leaves `/login`/`/settings` for any other page
  (the manager is done, till handed back), and `onResume` unconditionally
  re-locks on every foreground return regardless of prior unlock state.
  An earlier draft of this card watched for the WebView simply *landing*
  on `/settings` as its unlock signal — wrong on two counts: it would
  never fire for the `/login`-based self-order escape hatch at all (no
  navigation happens there), and it would unlock for any signed-in
  operator merely viewing Settings, not only a manager who actually used
  the PIN-gated exit action. Fixed before this shipped.

**Known limitation, by design of the mode itself:** without Device Owner
provisioning, Lock Task is standard Android *screen-pinning*, and a user
can still exit via Android's documented unpin gesture — **long-press Back
+ Overview together on 3-button navigation, or swipe up and hold on
gesture navigation** (review finding: an earlier draft of this doc named
only the 3-button gesture, which is wrong on the gesture-nav default most
current devices, including the TECLAST P50T test rig, actually ship with).
This is a real, weaker mode — deliberately shipped anyway because it
fixes the reported bug (previously the app was exitable like any normal
app) with zero device provisioning required.

**Scaffolded but NOT active:** `TillDeviceAdminReceiver` +
`res/xml/device_admin_receiver.xml` + the manifest `<receiver>` exist so
this app *could* later be provisioned as **Device Owner**, which
upgrades `startLockTask()` to full lock-task mode — no user-exit
gesture at all (`MainActivity.engageKioskLock` already detects
`isDeviceOwnerApp` at runtime and calls `setLockTaskPackages` first
when true; nothing else changes). **Known consequence, not yet mitigated:**
`setLockTaskPackages` allowlists ONLY this app's own package, so under
true Device Owner lock-task the Plugins page's "Import from file" side-load
(the document-picker `Intent` `MainActivity.fileChooserLauncher` launches)
will be blocked — the picker app itself isn't in the allowlist. Worth
allowlisting the device's documents provider too, or accepting the gap,
before actually provisioning a shop device this way; not addressed here
since Device Owner mode itself isn't active on any real device yet.
Provisioning is a manual, physical, one-time step on a device with no
accounts —
`adb shell dpm set-device-owner com.universaltill.pos/.TillDeviceAdminReceiver`,
or QR provisioning at factory reset — never attempted from code, and
not attempted in this session (no device available).

**Verification status:** none of this could be verified against a real
device or the Android SDK/emulator in the session that wrote it (neither
was available, and no download path existed). Real-hardware verification
on the actual TECLAST P50T test rig — per the ticket's own acceptance
criteria — is a required follow-up, tracked as its own board card.
Manual checklist for whoever does it:

1. Install the APK, launch it — confirm the app pins itself (a
   "screen is pinned" toast/hint appears on first pin) and the
   status/nav bars are hidden.
2. Press Home and open Recents — both must be no-ops; the notification
   shade must not open (a swipe from the top may transiently show the
   status bar, which then auto-hides — that's the expected immersive
   behavior, not a failure).
3. Perform the documented unpin gesture (long-press Back + Overview on
   3-button nav, or swipe-up-and-hold on gesture nav — check which
   navigation mode the device is actually using) —
   confirm it *does* unpin. That is the known screen-pinning limitation
   above, not a bug; note whether it matters for the pilot shop.
4. From the login screen's "exit to OS" collapsible (or the Settings
   page's own exit-to-os form) enter a correct manager PIN — confirm the
   success message appears AND the app unpins/bars return (the JS→native
   bridge firing, not just the server-side call succeeding).
5. Repeat from the *other* of those two forms (whichever #4 didn't use) —
   both must trigger the same native unlock.
6. Navigate away afterward (e.g. back to the sale screen, or self-order's
   own idle-reset redirect) — confirm the app re-pins and the bars hide
   again.
7. Background/foreground the app (Home, then relaunch — reachable while
   unpinned, or via the OS's own recent-apps affordance while pinned if
   any) and confirm `onResume` re-asserts the pin without crashing, even
   if step 4/5's unlock was still active.
8. From My Reports (`/my-reports`), tap the "open GitHub issue"
   link — confirm the WebView does NOT navigate to github.com (the
   navigation restriction refusing it, not the link being absent/disabled).
   This is the concrete case that motivated `shouldOverrideUrlLoading`.

## Not yet done

- Physical hardware integrations (printer/scanner/card reader) — the
  `runtime:"go"` process-spawning plugin type doesn't port to mobile at
  all (ADR-0023 §2); needs native adapters, not attempted here.
- A "stop the till" affordance in the UI itself (today the service just
  keeps running once started, matching a real POS terminal's actual
  requirement — but there's no in-app way to deliberately stop it yet
  short of force-stopping the app from Android's own app settings).
- A Play Store listing (deliberately not pursued — see "Signing &
  release" above).
