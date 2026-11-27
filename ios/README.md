# Universal Till for iPhone and iPad

The iOS/iPadOS till app (ADR-0023 §1, ut-docs#3046). Like the Android app
it is **not a rewrite**: the same Go till server every other platform runs
is bound into the app with `gomobile bind` (`../mobile`) and started
in-process; a small SwiftUI app shows it in a `WKWebView`. Everything the
operator sees and does is the server's web UI.

The web view locks page zoom (a user script sets the viewport to
`maximum-scale=1, user-scalable=no`), so the till neither pinch-zooms nor
zooms in when a field takes focus (ut-docs#3350); Safari keeps pinch.
`scripts/ci/ios-webview-zoom_test.sh` pins it.

## Build (a Mac with Xcode)

```bash
go install golang.org/x/mobile/cmd/gomobile@latest golang.org/x/mobile/cmd/gobind@latest
brew install xcodegen

# from the repo root: the pinned first-party Stripe + SumUp plugin bundles
# (hash-verified against internal/plugins/bundled/bundled.lock.json), then the
# Go server as an iOS framework with them embedded (ADR-0194, Apple 2.5.2:
# the App Store build never downloads code). CI does this on a GitHub-hosted
# runner; locally set UT_FETCH_ALLOW_LOCAL=1.
UT_FETCH_ALLOW_LOCAL=1 bash scripts/ci/fetch-bundled-plugins.sh
gomobile bind -target=ios,iossimulator -tags bundledplugins -o ios/build/Mobile.xcframework ./mobile
# the app icon asset catalog, from the canonical brand mark
bash ios/scripts/make-app-icon.sh

cd ios
xcodegen generate            # writes UniversalTill.xcodeproj from project.yml
open UniversalTill.xcodeproj # run on a simulator; a device needs your signing team
```

`UniversalTill.xcodeproj`, `UniversalTill/Info.plist`,
`UniversalTill/Assets.xcassets` and `build/` are generated and git-ignored;
`project.yml` is the source of truth.

**App icon.** `scripts/make-app-icon.sh` rasterizes
`web/public/assets/logo/unitill-logo.svg` (qlmanage + sips, built into
macOS) into a single 1024×1024 universal icon: the dark mark centred on a
white tile at 70% of its height, like the Android launcher icon, flattened
to opaque RGB — App Store Connect rejects an icon with an alpha channel.
Xcode derives the smaller sizes. `scripts/ci/ios-app-icon_test.sh` checks
the size, the missing alpha and the wiring.

CI (`.github/workflows/ios-ci.yml`) does the same on a GitHub macOS runner
for every change under `ios/**` or `mobile/**`, unsigned, then runs
`scripts/ci/ios-sim-smoke.sh`: it launches the app on an iPhone and an iPad
simulator, waits for the till server inside it to answer `/healthz`,
backgrounds and resumes it, and uploads screenshots as the
`ios-simulator-screenshots` artifact.

## Signing and TestFlight

`.github/workflows/ios-testflight.yml` archives the app (unsigned), then
signs it at export and uploads it to App Store Connect, where it lands in
**TestFlight for internal testing only** (owner + developers until v1.0,
ut-docs#3052). It never submits for App Store review.

- **When it runs:** for every release, `release.yml`'s publish-release job
  dispatches it with the release version; or by hand — Actions →
  *ios-testflight* → *Run workflow* (`gh workflow run ios-testflight.yml
  [-f version=0.27.0]`). It has no `release:` trigger on purpose: GitHub
  starts no workflow from the release.yml publish (made with GITHUB_TOKEN).
  Never on a push or pull request: the job holds the App Store Connect key,
  and a fork PR must never reach it.
- **Signing:** the archive is built unsigned; `xcodebuild -exportArchive
  -allowProvisioningUpdates` signs it with the team's Apple-managed (cloud)
  Distribution certificate and provisioning profile, authenticated by an
  App Store Connect API key (`ASC_KEY_ID`, `ASC_ISSUER_ID`, `ASC_KEY_P8` =
  the base64 of the `.p8`), in team `MACOS_NOTARY_TEAM_ID` (the same Apple
  team as the macOS notarization). No private key ever sits on the runner,
  so runs don't pile up certificates. The key is written under `$RUNNER_TEMP` with
  mode 600 and deleted when the job ends, pass or fail.
- **Versions:** the marketing version is the release tag without its `v`
  (a dispatch uses the `version` input, else the latest `v*` tag; a
  pre-release tag such as `v1.2.0-rc1` is refused). The build number is
  `<run number>.<run attempt>`, which only ever grows, so App Store Connect
  never sees a duplicate build.
- **Export compliance** is answered in `project.yml`
  (`ITSAppUsesNonExemptEncryption: false` — standard algorithms only), so
  builds don't wait on the questionnaire.
- **Who sees it:** after the upload the job waits (up to 40 minutes) for
  App Store Connect to finish processing the build, then **fails** unless
  an **internal** TestFlight group with at least one tester can see it
  (automatic distribution on, or the build added to the group) —
  `scripts/asc-testflight-check` (ut-docs#3217). The error names the
  missing piece: no internal group, a group with no testers, or no group
  that can see the build. A processing failure (`FAILED`/`INVALID`) or a
  build no tester can install (`MISSING_EXPORT_COMPLIANCE`,
  `PROCESSING_EXCEPTION`) fails it too. Fix it in App Store Connect (step 2
  below) and re-run. A **timeout** usually just means Apple is slow: the
  upload stands and still reaches testers once processed, and a re-run
  uploads a second build — check the TestFlight tab before re-running.
- `scripts/ci/ios-testflight-workflow_test.sh` (in `ci.yml`) pins the
  trigger, the release.yml dispatch, SHA-pinned actions, the unsigned
  archive, the repository guard, the hosted runner, the key handling and
  the tester check.

**Once, in App Store Connect** (the owner):

1. Before the first run: the app record must exist — Apps → **+** → New
   App, iOS, bundle ID `com.universaltill.pos` (register it under
   Certificates, Identifiers & Profiles first if it isn't listed), name
   *Universal Till* (done 2026-09-28). The API key is an **App Manager**
   key; if the first export is refused for certificate permissions, create
   an **Admin** key instead (cloud-managed distribution certificates).
2. After the first upload finishes processing (TestFlight tab): create an
   **internal testing group**, add the owner and the developers, and turn
   on **automatic distribution** so every later upload reaches them
   without another click.

## What the shell does

| Behaviour | Where |
|---|---|
| Data directory `Application Support/till` (sandboxed, in the device backup — except `till/secrets` and `till/tls`, which are `isExcludedFromBackup`, ut-docs#5140) | `TillServer.dataDirectory`, `TillServer.excludeSecretsFromBackup` |
| Start the server off the main thread; show its loopback address | `TillController.boot` |
| Navigation stays on the till's own origin; other links open in Safari/Mail | `TillWebView.Coordinator` |
| Camera (barcode viewfinder; item photos use the system camera via the file input, ut-docs#4096) granted to the till's own page only | `requestMediaCapturePermissionFor` |
| A failed page load shows an error bar with **Back to till** | `ContentView` |
| On return from the background, probe `/healthz`; restart the server only if iOS reclaimed its socket (Apple TN2277) — a healthy server is never restarted | `TillController.resume` |
| On return from the background, once the server answers, ask the till's cloud-credential self-heal to check now (a suspended app ran no timers), at most every 30 s; never restarts or touches the server (ut-docs#7097, ADR-0222) | `TillController.resume`, `TillServer.notifyForeground`, `ForegroundThrottle` |
| A WebView process killed in the background is reloaded | `webViewWebContentProcessDidTerminate` |
| All orientations on iPhone and iPad | `project.yml` |

The server binds every interface so other tills can pair with this one
(ADR-0033); iOS asks the operator once for **local network** access.

LAN discovery (finding a main till, being found as one, network printers)
goes through Apple's Bonjour APIs, not Go's own mDNS sockets: raw multicast
would need Apple's managed `com.apple.developer.networking.multicast`
entitlement. `BonjourBridge.swift` implements the Go side's
`mobile.DiscoveryBridge` — `NWBrowser` to browse (a UDP `NWConnection`
resolves each result, sending nothing), `DNSServiceRegister` to advertise —
and `TillServer.start` registers it before the server starts (ut-docs#3218).
A refused Local Network permission shows its own message on the Tills page
and the setup wizard, pointing at Settings and at pairing by code.

## Bug-report screenshots (ut-docs#3355)

A `WKWebView` has nothing for `getDisplayMedia` to record, so the panel's
"Take screenshot" used to capture only the system share prompt.
`UniversalTill/ScreenshotBridge.swift` registers a `utScreenshot`
script-message handler with reply (page content world): the panel calls
`window.webkit.messageHandlers.utScreenshot.postMessage('capture')`, hides
itself for two frames, and gets back `WKWebView.takeSnapshot` of the till
page as a PNG data URL (or `""` on failure — the same contract as Android's
`AndroidKiosk.captureScreenshot()`). Only the till origin's main frame is
answered. `scripts/ci/ios-screenshot-bridge_test.sh` pins the wiring;
`e2e/tests/ios-screenshot-bridge-3355.spec.ts` drives the panel side.

## Tap to Pay on iPhone (development)

Card payments on the iPhone itself, with no reader (ut-docs#3049, ADR-0125
§3, Stripe first). **Off by default**, and only a developer's own
dev-signed device build can turn it on: Apple's Tap to Pay grant for team
B45H898ZBK is development-only (ut-docs#3047), so TestFlight and Release
never carry the entitlement (ut-docs#6460 moves it there later).

How it fits together. The web page never starts a payment and never sees a
result, so the approval verdict never lives in JS. The chain is: payment
plugin → host fn (ut-docs#6455) → `internal/devicepay` →
`mobile.TapToPayBridge` → `UniversalTill/TapToPay/TapToPayBridge.swift` →
`TapToPayCoordinator` → Stripe Terminal iOS SDK 6.0.0 (pinned; the
`Package.resolved` under `UniversalTill.xcodeproj/` is committed). The
Swift side reaches ut-cloud only through `mobile.TapToPayCloud` →
`internal/paybroker` (fixed op allow-list, the till's own device
credential, never logged). iPad is never offered Tap to Pay (capability
reason `ipad`).

**The switch.** `UT_TAP_TO_PAY` in `project.yml` (default `NO`):

- The code is always compiled, so CI builds every line. Only the bridge's
  registration is gated (`TapToPayFeature.isEnabled`). With the flag off
  nothing registers, Go reports no capability, and nothing is shown.
- `YES` in the **Debug** configuration also signs
  `UniversalTill/UniversalTill.dev.entitlements`
  (`com.apple.developer.proximity-reader.payment.acceptance`). Release
  hard-sets `CODE_SIGN_ENTITLEMENTS` to empty and `UT_TAP_TO_PAY_INFO` (the
  value Info.plist's `UTTapToPayEnabled` gets, which is what the app reads)
  to `NO`. So a Release build carries no entitlement and registers nothing,
  even with `UT_TAP_TO_PAY=YES` on the command line.
- `UT_TAP_TO_PAY_SIMULATED=YES` uses Stripe's simulated reader (no card, no
  NFC hardware needed).
- `scripts/ci/guard-ios-tap-to-pay-default-off.sh` (in `ci.yml`) fails a PR
  that assigns the flag anything but `NO` under `.github/`, `ios/`,
  `scripts/` or in the `Makefile` (this README excepted), un-pins Release,
  or adds another entitlement. Never commit the flag on.
- CI and TestFlight build with `-onlyUsePackageVersionsFromResolvedFile`, so
  the Stripe SDK is only ever the revision in the committed
  `Package.resolved`. To move the SDK: change `exactVersion` in
  `project.yml`, run `xcodegen generate` and
  `xcodebuild -resolvePackageDependencies -project UniversalTill.xcodeproj`,
  and commit the new `Package.resolved`.

**Once, in the Apple developer portal** (the owner, Account Holder):

1. Certificates, Identifiers & Profiles → Identifiers →
   `com.universaltill.pos` → Additional Capabilities: confirm **Tap to Pay
   on iPhone** is enabled.
2. Devices: register the test iPhone's UDID (Finder or Xcode → Devices
   shows it) on team B45H898ZBK.
3. Regenerate the **development** provisioning profile. Xcode's automatic
   signing does this on the next device build once steps 1 and 2 are done.
   A manually managed profile must be regenerated in the portal.

**A dev-signed device build** (the MacBook, Xcode signed in to the team):

```bash
gomobile bind -target=ios,iossimulator -o ios/build/Mobile.xcframework ./mobile
bash ios/scripts/make-app-icon.sh
cd ios && xcodegen generate
# either on the command line, with the iPhone plugged in:
xcodebuild -project UniversalTill.xcodeproj -scheme UniversalTill -configuration Debug \
  -destination 'platform=iOS,name=<your iPhone>' -allowProvisioningUpdates \
  DEVELOPMENT_TEAM=B45H898ZBK UT_TAP_TO_PAY=YES build
# or in Xcode: open UniversalTill.xcodeproj, target UniversalTill → Build
# Settings → User-Defined → UT_TAP_TO_PAY = YES (Debug). The generated
# project is git-ignored, so the change never reaches a commit. Then Run on
# the iPhone.
```

Check what got signed:
`codesign -d --entitlements - <path to UniversalTill.app>` should list the
proximity-reader key for that build and for no other.

**Unit tests** (the coordinator's state machine, fake reader, cloud and
clock; no device, no xcframework):

```bash
cd ios && xcodegen generate && xcodebuild test -scheme UniversalTillTapToPayTests \
  -destination 'platform=iOS Simulator,name=iPhone 18 Pro'
```

`ios-ci.yml` runs the same tests before the gomobile bind.

### Device test checklist (iPhone 17 Pro Max, Stripe test mode)

For each check, record what you expected and what you saw, with a screenshot
or video. A screen that merely shows something is not evidence.

**Testable now** (this change alone; there is no payment entry or settings
panel yet):

| # | Do | Expect |
|---|---|---|
| A | Flag-off Debug build on the iPhone | Launches as before. No location or Bluetooth prompt, no banner, nothing about Tap to Pay. `codesign -d --entitlements -` shows no proximity-reader key. |
| B | Latest TestFlight build | `codesign -d --entitlements -` on the installed or archived app shows no proximity-reader key. |
| C | Flag-on Debug build (portal steps done) | Xcode signs it without a provisioning error, and `codesign` shows `com.apple.developer.proximity-reader.payment.acceptance` = true. The app launches and the till works normally. There are no prompts and no banner, because nothing calls Setup yet and prewarm waits for a completed Setup. |
| D | Flag-on build: background, then foreground, five times | No crash, no prompt, and the till stays usable. |
| E | Flag-on build on an iPad | Launches normally and shows nothing about Tap to Pay. |
| F | Unit tests (above) | All tests pass (169 today). |
| G | Release build with `UT_TAP_TO_PAY=YES` on the command line, unsigned | `UTTapToPayEnabled` in the built `Info.plist` is `NO`, and `-showBuildSettings` shows an empty `CODE_SIGN_ENTITLEMENTS`. |

**After #6455 (host fn), #6456 (plugin), #6454/#3055 (cloud broker) and
#6458 (settings UI) land.** Prerequisites: a test connected account; a
business Apple ID (not a sandbox ID); the iPhone has a passcode and runs a
public (non-beta) iOS; a Stripe physical test card (mobile wallets don't
work in a sandbox, https://docs.stripe.com/terminal/references/testing).

1. A flag-off build shows no Tap to Pay anywhere. The TestFlight build has
   no entitlement (`codesign -d --entitlements -`).
2. iPad with the flag on: Settings shows "Not available" plus the iPad
   line. There is no pay entry.
3. New user. A manager taps **Set up**. The location prompt shows our text.
   Apple's terms are accepted. The determinate progress banner shows. How
   to Tap opens automatically. The chip reads Ready. A cashier can't see
   the panel, and a direct POST gets 403.
4. Existing user. Relaunch, and background then foreground: prewarm
   reaches Ready with no terms screen.
5. £10.00 is approved. The sale is recorded as "Tap to Pay on iPhone" with
   brand and last 4 on the receipt. The PaymentIntent is on the
   **connected account**, with no application fee and `metadata.ut_key`
   set.
6. £10.05 is declined (`generic_decline`). The basket stays open and no
   sale is recorded.
7. £10.03 asks for a PIN and is then approved.
8. The customer dismisses the sheet: cancelled, with no charge.
9. No card for over 120 s: the sheet closes and the result is not
   "declined". The PaymentIntent is not succeeded.
10. Airplane mode at the tick: "Payment not confirmed". After reconnecting,
    **Check again** gives one PaymentIntent, no second tap, and the right
    outcome.
11. A decline, then a retry on the same basket: the same PaymentIntent is
    reused.
12. Offline before checkout: the entry is disabled with its chip, and a
    cash sale completes.
13. Location denied: the reason chip shows, with no crash.
14. Rotation or an incoming call during collect: the result is delivered,
    or it is cancelled or pending. There is never a lost or duplicate
    charge.
15. VoiceOver and Dynamic Type XXL on the chip and the banner.
16. Refund a tap sale from the till: the refund appears on the connected
    account.

## Phone card-reader mode (development, ut-docs#6538)

The iPhone can also be a **card reader for the shop's main till** (ADR-0198 §6): the
main creates the PaymentIntent, sends the collect over the pinned LAN link, this
phone taps and answers; the main confirms with the payment service. It is a
Swift port of Android's `reader/` package (`UniversalTill/Reader/`, pure parts in
`Reader/Core/`, tested against the shared goldens `internal/fleetlink/testdata/reader_*.json`).

- **Default OFF.** `UT_READER_MODE` (project.yml) is `NO`; a Debug dev-signed
  device build turns it on with `UT_READER_MODE=YES` **and** `UT_TAP_TO_PAY=YES`
  (same development-only entitlement). Release pins it to `NO`
  (`scripts/ci/guard-ios-tap-to-pay-default-off.sh` checks it). With it off there
  is no reader entry, no Keychain item, no socket and no reader Bonjour browse.
- **One collect lock.** The phone's own tender and a reader collect share the
  Tap to Pay coordinator's single `current` attempt: whichever starts second is
  refused (`busy`), never queued. The Stripe SDK has one token provider
  (`DelegatingTokenProvider`): the till's own credential through the cloud while the
  till's own tender runs, the main's `reader.token` while reader mode owns the reader.
- **Credential** (bearer, pin, address, reader id): Keychain, this device only,
  while unlocked, never synced. No card data, no shop data, no client secret is stored or logged.
- **Foreground only.** iOS suspends the socket in the background and there is no
  push wake: a locked phone is unreachable and the main says so.
- Pairing needs the main's `phone_pairing` setting on and a TLS-pinned LAN link;
  the pin is checked in the TLS server-trust challenge before any request is
  written (P-256 only, anything else is refused).
- Tests: the same `UniversalTillTapToPayTests` scheme (host-less, no Stripe, no Go framework).
  `go run ios/scripts/gen-pin-vector.go` regenerates `UniversalTillTests/Fixtures/pin_vector.json`.
- Not verifiable without the iPhone: a real tap from a `reader.collect`, Apple's sheet
  making the app inactive, Local Network discovery, the socket on a locked screen.

## What doesn't port (and what isn't built yet)

- **`runtime:"go"` process plugins don't run** — iOS never lets an app
  spawn another process (same limit as Android, ADR-0023 §2). WASM plugins
  run unchanged (wazero's interpreter).
- **Android-only bridges have no iOS counterpart yet** — file downloads
  (exports), the self-order kiosk lock (Guided Access) and a BLE
  receipt-printer bridge: ut-docs#3071.
- **No in-app Bluetooth pairing, by design** (ut-docs#3261): iOS keeps
  scanners and keyboards (HID) for the system and never exposes them to an
  app through Core Bluetooth, so they pair in **Settings → Bluetooth** and
  type into the WebView like a keyboard. The Bluetooth devices page says so
  on iOS instead of offering a Scan button (not yet device-checked,
  ut-docs#3070). Bluetooth receipt printing has no transport on any
  platform yet; the iOS printer bridge is part of ut-docs#3071.
- **Not device-checked yet** — offline sale, rotation, long background,
  pairing on a real iPhone/iPad: ut-docs#3070.
