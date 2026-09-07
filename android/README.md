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
  (`network_security_config.xml`), which keeps the app from accepting
  cleartext to anything else it might ever talk to. The embedded server
  itself binds **all** interfaces (`0.0.0.0`, ut-docs#1256) so an Android
  till is LAN-reachable — discoverable and pairable as a primary by other
  tills, behind the same ADR-0033 discovery + approve-to-pair +
  per-till bearer-token boundary the Linux/Pi **service** till (the bare
  `unitill-pos` binary) already ships — NOT `cmd/unitill-desktop`'s WebView
  shell, which stays loopback-only like this app always has. That
  doesn't change what the config must permit: Network Security Config
  governs only the app's own outbound Java/WebView-layer cleartext calls,
  and the WebView still only ever loads `127.0.0.1`; inbound connections
  to the Go server's socket (and the Go server's own outbound LAN calls)
  go through the Go runtime directly, outside Network Security Config.

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

## Kiosk lock-down (ut-docs#1254, corrected by ut-docs#1508)

What's implemented now, all in `MainActivity`:

- **Screen-pinning Lock Task** (`startLockTask()`) engages ONLY while the
  WebView is showing a `/self-order*` page — never for ordinary till use
  (the sale screen, the setup wizard, `/login`, `/settings`). Product
  owner's rule (ut-docs#1508, verbatim in spirit): "for the till, we can
  only hide the bottom OS menu but let it go to the OS. Only it cannot go
  to the OS if it is on the self-ordering mode." `onPageFinished` decides,
  from which page class navigation just landed on, whether to engage or
  release the pin; the resulting intent is recorded in `kioskPinned`, and
  `onResume` only ever RE-ASSERTS that state on a foreground return —
  never decides one of its own (defense in depth — all calls are
  idempotent). That split matters both ways: a resume can't pin a till
  that wasn't pinned (the ut-docs#1508 bug), and it can't drop a genuine
  self-order pin that no manager PIN has released (independent review:
  deriving the resume decision from the *current URL* instead would have
  unpinned a kiosk parked on the `/login` prompt — one tap from the
  self-order screen's 🔒 exit link — as soon as the screen blinked off
  and on). Home and Recents are blocked only while actually pinned.
  **Before this ticket**, the pin engaged unconditionally on every screen
  including the pre-enrollment setup wizard — which is how the product
  owner got bricked on 2026-09-03: the wizard hit a bare JSON error page
  (ut-docs#1507, a *deliberate* `guard-page-http-error.sh` exception —
  the wizard has no operator layout to render an escape link into) with
  Home/Recents both blocked by a pin that should never have engaged
  there. **Not yet done** (split out of ut-docs#1508's own acceptance
  criteria and tracked as its own card, ut-docs#1513): a physical-button
  and remote unlock path for
  self-order specifically, so an operator isn't limited to the documented
  unpin gesture below or a working web UI even while genuinely
  self-order-pinned — both need new cross-repo design (a hardware
  decision, and an ut-cloud-side remote channel) out of scope for this fix.
- **Immersive full-screen** (androidx `WindowInsetsControllerCompat`)
  hides the status and navigation bars as a cosmetic second layer,
  **in every mode, self-order or not** — a swipe reveals them
  transiently and they auto-hide again. This is what "hide the bottom OS
  menu but let it go to the OS" means in ordinary till mode: the bar is
  hidden, but nothing (no Lock Task) stops the operator reaching it.
- **Navigation is confined to the till's own loopback origin**
  (`WebViewClient.shouldOverrideUrlLoading`) — a link this app doesn't
  control the far end of can't navigate this WebView off-origin. This
  isn't only kiosk hygiene: the exit bridge below (`addJavascriptInterface`)
  is only safe to expose because of it.
- **Off-origin `http(s)` links go to the system browser instead**
  (`MainActivity.openInSystemBrowser`, ut-docs#1647) — as a separate task
  (`FLAG_ACTIVITY_NEW_TASK`), leaving the WebView on the POS screen it was
  already showing. Blocking alone was the whole behaviour until this
  landed, and `shouldOverrideUrlLoading` returning `true` means "handled",
  so every external link in the till UI (`/my-reports`'s "View on GitHub",
  the catalog/inventory "open primary" banners) was **silently dead** on
  Android — the product owner hit this on a real tablet. Only `http` and
  `https` are forwarded: `Intent.ACTION_VIEW` resolves whatever app claims
  a scheme, so passing an arbitrary one through would turn any link on any
  page into an app-launch primitive. **Main-frame navigations only** — this
  callback also fires for subframes, so without that test one off-origin
  `<iframe>` anywhere in the till UI (or in a plugin-rendered page) would
  spawn a browser on every page load, over the live sale screen. There is
  deliberately **no kiosk release** here — Lock Task silently refuses a non-allowlisted activity,
  so this is a no-op in a pinned self-order session, which is correct:
  dropping the pin for an untrusted external URL would be a kiosk escape
  reachable from page content. Nothing is lost in practice, because every
  page carrying an external link is manager-gated and unreachable from the
  self-order kiosk. Guarded by `scripts/ci/guard-android-external-links.sh`.
  macOS has behaved this way since it shipped
  (`cmd/unitill-desktop/webkit_darwin.go`); Linux/Windows are unverified —
  ut-docs#1649.
- **Exit from self-order = the server's own existing "exit to OS" escape
  hatch, no native-side auth.** `/api/settings/exit-to-os`
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
  same WebView, and the bridge, to an untrusted origin). **Since
  ut-docs#1508**, `/login`/`/settings` are already unpinned on arrival
  whenever the till wasn't self-order-pinned to begin with (ordinary till
  use never pins at all) — `exitLockdown()` still matters for the one
  case that needs it: releasing a **genuine self-order pin** on the way
  into a manager PIN prompt reached from the self-order screen itself.
  That pin deliberately SURVIVES the navigation to `/login`/`/settings`
  (`onPageFinished` leaves the lock state alone on those two page
  classes) and every background/foreground cycle, until either the PIN
  goes in or navigation lands somewhere outside self-order — otherwise
  merely tapping the 🔒 exit link would be the escape the PIN exists to
  gate.

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

**Worse than that, in fact (ut-docs#1281/#1639): on an unprovisioned
device the pin can fail to engage AT ALL, and it looks exactly like a
locked kiosk when it does.** `startLockTask()` only *requests* the pin —
Android answers with a system confirmation dialog
("App is pinned — touch & hold Back and Overview to unpin… No, thanks /
Got it") that this app cannot see, dismiss, or pre-answer. Measured on the
TECLAST P50T test rig: four consecutive cold launches into self-order
mode all sat at `lockTaskModeState=NONE` for 12s+ with the dialog
unanswered, and tapping "No, thanks" leaves it `NONE` forever. Immersive
mode (the hidden status/nav bars) succeeds independently of the pin, so
an unpinned kiosk is **visually indistinguishable** from a pinned one —
from that state, one press of Home reaches the Android launcher (every
app on the device: Chrome, Files, Settings, whatever else is installed)
with the unanswered dialog still sitting on top of it.

**What ut-docs#1639 adds on top of the above** (still all in
`MainActivity`, still screen-pinning only — this does not change which
mode engages, only whether the app tells the truth about it):
- `verifyPinEngaged()`, first scheduled a few seconds after every
  `engageKioskLock()` call, reads `ActivityManager.getLockTaskModeState()`
  directly rather than trusting `startLockTask()` having returned. **It
  then re-arms itself every 3s**, for as long as a pin is intended and only
  between `onResume` and `onPause`, so both late answers and later losses of
  the pin are noticed. The lifecycle bound is enforced by `schedulePinCheck`
  refusing to arm at all outside that window — cancelling in `onPause` is
  NOT sufficient on its own, because `onPageFinished` has no lifecycle
  gating and this app never pauses the WebView's JS timers, so the
  self-order idle reload goes on re-entering `engageKioskLock()` while the
  Activity is stopped (independent review, ut-docs#1639).
- `onWindowFocusChanged(hasFocus)` re-reads the same state as soon as the
  app gets input focus back, as an opportunistic fast path on top of that
  poll. It is deliberately asymmetric: regaining focus can clear a
  warning, but only raises one for a pin that was previously **confirmed**
  and has since gone (the documented unpin gesture) — focus can also come
  back while the request is merely still unanswered, and that is the
  repeating check's call to make, not this one's.
  **No Activity-level lock-task callback exists** to use instead
  (independent review, ut-docs#1639: an earlier draft of this fix
  overrode a non-existent `Activity.onLockTaskModeChanged(int)` — the
  platform's only lock-task callbacks are
  `DeviceAdminReceiver.onLockTaskModeEntering/Exiting`, which need the
  Device Owner provisioning below).

  **Why the poll exists, and why focus alone was not enough.** The first
  version of this fix made `onWindowFocusChanged` the mechanism that
  cleared the banner, on the documented reasoning that Android's pinning
  confirmation is a system window taking focus without pausing the
  Activity — so dismissing it must hand focus back. On the P50T that does
  not produce a usable read. Re-verifying this card on real hardware
  (v0.12.7): tapping **Got it** pinned the app — Android's own "App
  pinned" toast, `mLockTaskModeState=PINNED` — and the banner stayed up
  across three runs, with neither of that callback's logging branches
  reached. The build that was measured could not say whether no focus event
  arrived at all or one arrived before the OS had committed the lock-task
  state, because its "not answered yet" branch logged nothing. That branch
  now logs at debug level, and **that settled it on the device: the
  callback does not fire at all when the pinning dialog is dismissed.** The
  same build emits that debug line reliably for a genuine focus return
  (sleep/wake with the dialog still up) and emitted nothing at all when
  "Got it" was tapped — an absent event, not an early read. Either way the
  state was read once, at the wrong moment, and never again. Nothing else re-read it, so a correctly
  pinned till went on displaying *"Kiosk lock is not active — this device
  is not secured"*; measured still up 120s after the grant, with the
  banner's only escape being an unrelated accident: `self_order.html`'s
  idle-reset reload re-running `engageKioskLock()` through
  `onPageFinished`. That reload is opt-**out** — `kiosk.idle_reset_seconds`
  defaults to 60 and only `0` disables it — so relying on it would not even
  have been a rare configuration. The 120s figure holds because the
  reload's timer restarts on every `pointerdown`/`keydown`/`touchstart`,
  and that run tapped the screen every 20s to keep it from firing; an idle
  kiosk would have had the banner cleared for it at 60s, by a mechanism
  with nothing to do with pinning that a shop can switch off.
  A permanent false alarm is the same defect as a missing one — staff
  who see "not secured" on a secured till stop reading the banner. Since
  the platform offers no callback here without Device Owner, re-reading
  on a timer is the only mechanism actually available.
- Either path logs the outcome (`Log.w`/`Log.i`, tag `MainActivity`) —
  before this fix, `logcat` showed nothing at all for a failed pin.
- When the pin is intended (self-order) but not confirmed, a red banner
  (`R.string.kiosk_pin_not_engaged`, translated en/fa/tr/ar) appears above
  the WebView — a status banner, not a modal, so the kiosk stays fully
  usable underneath. It clears within one poll interval of the OS
  confirming the pin, and comes back within one poll interval of the pin
  being lost again.
- **Not fixed, because it cannot be from app code**: the confirmation
  dialog itself, or getting it answered automatically. The banner and log
  line are detection and disclosure, not a stronger lock — **Device Owner
  provisioning (below) is still the only way to get a pin that cannot be
  declined**.

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

1. Install the APK, launch it — confirm the app does **NOT** pin itself
   (no "screen is pinned" toast/hint) but the status/nav bars ARE
   hidden. Press Home — it must work normally, returning to the
   launcher; relaunch the app and confirm it comes back to the sale
   screen, still unpinned. This is the ut-docs#1508 behavior change:
   ordinary till use is never pinned.
2. If the till is mid-setup (first boot, before enrollment), drive the
   wizard through a step that fails (e.g. disconnect network briefly on
   a step that calls out) — confirm the resulting error page, even if
   it's a bare/unstyled one, still leaves Home/Recents reachable. This
   is the concrete incident ut-docs#1508 reports: a wizard error page
   used to be an unrecoverable dead end because the pin was on.
3. From the sale screen, switch this device to **Self-order kiosk**
   (Settings → Display → device profile, manager PIN) and confirm that
   once the screen lands on `/self-order`, the app now pins itself (the
   "screen is pinned" hint) and the status/nav bars are hidden.
4. With the self-order screen pinned, press Home and open Recents —
   both must be no-ops; the notification shade must not open (a swipe
   from the top may transiently show the status bar, which then
   auto-hides — expected immersive behavior, not a failure).
5. Perform the documented unpin gesture (long-press Back + Overview on
   3-button nav, or swipe-up-and-hold on gesture nav — check which
   navigation mode the device is actually using) — confirm it *does*
   unpin. That is the known screen-pinning limitation above, not a bug;
   note whether it matters for the pilot shop.
6. Re-pin (repeat step 3), then tap the self-order screen's 🔒 manager
   entry point into `/login` **without** entering a PIN — confirm the
   app is STILL pinned there (Home/Recents still no-ops), then press the
   power button to blank the screen and wake it again and confirm it is
   still pinned afterwards. This is the independent-review case: the
   `/login` prompt is one tap from the customer-facing screen, so if a
   screen blink released the pin, the PIN gate would be decorative.
   Let the kiosk idle-reset timer run out too — it must bounce back to
   `/self-order`, still pinned.
7. Now enter a correct manager PIN on that same `/login` prompt —
   confirm the success message appears AND the app unpins/bars return
   (the JS→native bridge firing, not just the server-side call
   succeeding), landing on a manager screen that is itself NOT pinned.
8. From that manager screen, switch the device profile back to
   **Register** and confirm the sale screen loads unpinned, with no
   stray pin left over from the self-order session.
9. Background/foreground the app at each of the states above (Home,
   then relaunch) and confirm `onResume` re-asserts exactly the pin
   state that state was in — pinned on `/self-order` and on a `/login`
   reached from it, unpinned everywhere else — without crashing.
10. From My Reports (`/my-reports`), tap the "View on GitHub" link —
    confirm **both halves** (ut-docs#1647): the issue opens in the device's
    browser app, as a separate task, AND the till's own WebView is still on
    `/my-reports` behind it (it must never navigate to github.com itself —
    that restriction is what keeps `window.AndroidKiosk` away from a page
    this app didn't author). Checking only that the WebView stayed put is
    what let the "tapping it does nothing" bug ship: both outcomes look
    identical from the till's side of the screen.

**ut-docs#1639 verification status:** items 1-10 above were run for real
on the TECLAST P50T (ut-docs#1281) and confirmed the pin-detection gap
this section now describes. The detection/log/banner code added for
ut-docs#1639 itself could **not** be compiled or run in the session that
wrote it (same constraint as ut-docs#1508's own review — no Android
SDK/NDK/emulator in that session; see
`docs/code-reviews/2026-09-03-kiosk-pin-self-order-only-1508.md`'s
"Verified beyond automated tests" for the accepted precedent) — verified
instead by close reading against the actual AOSP
`android/app/Activity.java` source and documented `ActivityManager`
semantics, plus this file's own existing patterns. (That reading is not a
formality: it is how the independent review found the first draft's
`onLockTaskModeChanged` override, which would not have compiled and which
**no PR check in this repo would have caught** — nothing in `ci.yml`
builds the Gradle project; only `release.yml`'s `android-app` job does,
i.e. the failure would have surfaced as a broken release.) **Required
follow-up on real hardware**, whoever has the TECLAST P50T next:

11. Repeat step 3 (enter self-order) and tap **"No, thanks"** on the
    pinning confirmation the instant it appears — confirm the red
    `kiosk_pin_not_engaged` banner appears above the WebView within
    ~3 seconds, and `adb logcat -s MainActivity` shows the
    `lockTaskModeState=NONE` warning line.
12. Repeat step 3 and tap **"Got it"** (or just wait) so the pin actually
    engages — confirm the banner never appears (or appears then clears
    within ~3s) and logcat shows the "confirmed engaged" line instead.
13. Repeat step 3 but leave the pinning confirmation sitting unanswered
    for ~10 seconds (longer than the 3s delay), then tap **"Got it"** —
    confirm the banner appears at ~3s and then **clears on its own** the
    moment the dialog is dismissed, without needing to leave and re-enter
    self-order. This is the `onWindowFocusChanged` re-check; it is the
    one behaviour that has no automated coverage of any kind.
14. From a genuinely pinned self-order screen (step 12), perform the
    documented unpin gesture, then bring the app back to the foreground
    (power off/on, or Recents) — confirm the banner is up, and that the
    logcat line says `DROPPED`, not `NOT engaged`. Note in the ticket
    whether the banner appeared *without* that foreground round-trip:
    Android gives the app no callback for a pin dropping, so a device
    that does not change window focus on unpin will not show it until
    something else re-checks (a resume, or the next `/self-order`
    navigation). That gap is expected, and is what a Device-Owner pin
    (which cannot be unpinned at all) would remove.

## Camera, microphone and screenshots (ut-docs#1435)

Two gaps in the bug-report panel (🐞) on Android, both in `MainActivity`:

- **Screenshots** — Android's WebView implements no `getDisplayMedia` at
  all, so the panel's screenshot button (a one-frame display capture on
  every other platform) could only ever show "not available here".
  `KioskBridge.captureScreenshot()` is a second `window.AndroidKiosk`
  bridge method: a synchronous native capture of the WebView's current
  content, returned as a `data:image/png;base64,...` URL (`""` on any
  failure — nothing ever throws across the bridge). `PixelCopy` of the
  Activity window, clipped to the WebView's own bounds, on API 26+;
  `View.draw` onto a software canvas on API 24-25. The bridge method
  runs on a WebView background thread and blocks on a `CountDownLatch`
  (5 s timeout) for the UI-thread copy, then encodes the PNG on that
  background thread — keeping the Android UI thread free, though the
  call is still synchronous from the page's own JS, so the panel itself
  is unresponsive for the (normally sub-second) capture. See the
  bridge method's own KDoc for the corrected version of this claim.
  `bugreport_panel.html` prefers this bridge whenever it exists and
  falls through to its unchanged `getDisplayMedia` branches otherwise.
  Exposure is safe on the same grounds as `exitLockdown` — navigation
  is confined to the till's own origin, so the only thing capturable is
  the page the operator is already looking at.
- **Microphone (and camera) for `getUserMedia`** — a plain WebView's
  `WebChromeClient.onPermissionRequest` default is an unconditional
  `deny()`, and the app declared no CAMERA/RECORD_AUDIO permission, so
  the panel's voice note was silently refused on every Android till
  (the panel's own "mic error" text, never an OS prompt). Now
  `onPermissionRequest` grants — **only for the till's own loopback
  authority, failing closed while it is unknown**, mirroring
  `shouldOverrideUrlLoading` — after checking/requesting the real Android
  runtime permissions via a `RequestMultiplePermissions` launcher, holding
  the `PermissionRequest` across the dialog and resolving it with exactly
  the subset the OS actually granted. Requested lazily, only when a page
  starts a capture; never at boot (unlike `POST_NOTIFICATIONS`). The
  manifest declares `CAMERA`, `RECORD_AUDIO`, `MODIFY_AUDIO_SETTINGS`, and
  deliberately **no** `<uses-feature android.hardware.camera>` — a till
  without a camera must stay installable.

**Not in this change (follow-up cards):** screen *recording* on Android
(needs MediaProjection + a `mediaProjection` foreground service — the
panel still says "not available here" for it), and the catalog page's
in-page camera viewfinder (the `CAMERA` half of the plumbing above is
wired but has no first consumer yet).

**Verification status:** as with the kiosk work above, not compiled or
run against a device/SDK in the session that wrote it. The panel-side
wiring IS driven for real by `e2e/tests/android-screenshot-bridge-1435.spec.ts`
(a stubbed `window.AndroidKiosk`, both bridge outcomes, and the desktop
path proven untouched). Manual checklist for the TECLAST P50T:

1. Open 🐞 on the sale screen, press "📷 Take screenshot" — a thumbnail
   of the sale screen must appear within a second, with no OS prompt.
   Press it several times; remove one with ✕. Save the report and confirm
   the image is attached under My Reports.
2. Press "🎤 Record voice note" for the first time — Android's own
   microphone permission dialog must appear (this is the FIRST time the
   app has ever asked; it must not have asked at launch). Allow it:
   recording starts, stop it, the preview plays.
3. Press it again — no dialog this time, recording starts immediately.
4. Revoke the microphone permission under Settings → Apps → Universal
   Till → Permissions, return to the till and press record again — the
   dialog (or, after a "don't ask again", the panel's mic-error text) must
   appear; the app must not crash and the till underneath must keep
   working.
5. Confirm the "Record screen" button still reads "not available here"
   — expected until the MediaProjection follow-up lands.

## Not yet done

- Screen recording in the bug-report panel on Android (MediaProjection)
  and an in-page camera viewfinder for catalog photos — both split out of
  ut-docs#1435 as their own cards; see that section above.

- Physical hardware integrations (printer/scanner/card reader) — the
  `runtime:"go"` process-spawning plugin type doesn't port to mobile at
  all (ADR-0023 §2); needs native adapters, not attempted here.
- A "stop the till" affordance in the UI itself (today the service just
  keeps running once started, matching a real POS terminal's actual
  requirement — but there's no in-app way to deliberately stop it yet
  short of force-stopping the app from Android's own app settings).
- A Play Store listing (deliberately not pursued — see "Signing &
  release" above).
