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

# from the repo root: the Go server as an iOS framework
gomobile bind -target=ios,iossimulator -o ios/build/Mobile.xcframework ./mobile
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
| Data directory `Application Support/till` (sandboxed, in the device backup) | `TillServer.dataDirectory` |
| Start the server off the main thread; show its loopback address | `TillController.boot` |
| Navigation stays on the till's own origin; other links open in Safari/Mail | `TillWebView.Coordinator` |
| Camera (barcode viewfinder) granted to the till's own page only | `requestMediaCapturePermissionFor` |
| A failed page load shows an error bar with **Back to till** | `ContentView` |
| On return from the background, probe `/healthz`; restart the server only if iOS reclaimed its socket (Apple TN2277) — a healthy server is never restarted | `TillController.resume` |
| A WebView process killed in the background is reloaded | `webViewWebContentProcessDidTerminate` |
| All orientations on iPhone and iPad | `project.yml` |

The server binds every interface so other tills can pair with this one
(ADR-0033); iOS asks the operator once for **local network** access.

## What doesn't port (and what isn't built yet)

- **`runtime:"go"` process plugins don't run** — iOS never lets an app
  spawn another process (same limit as Android, ADR-0023 §2). WASM plugins
  run unchanged (wazero's interpreter).
- **mDNS discovery needs Apple's multicast entitlement**
  (`com.apple.developer.networking.multicast`), which is requested per app
  from Apple — until then an iOS till neither advertises nor finds other
  tills; pairing by code / IP address works regardless. Tracked in
  ut-docs#3069.
- **Android-only bridges have no iOS counterpart yet** — file downloads
  (exports), the bug-report screenshot, the self-order kiosk lock (Guided
  Access) and a BLE receipt-printer bridge: ut-docs#3071.
- **No in-app Bluetooth pairing, by design** (ut-docs#3261): iOS keeps
  scanners and keyboards (HID) for the system and never exposes them to an
  app through Core Bluetooth, so they pair in **Settings → Bluetooth** and
  type into the WebView like a keyboard. The Bluetooth devices page says so
  on iOS instead of offering a Scan button (not yet device-checked,
  ut-docs#3070). Bluetooth receipt printing has no transport on any
  platform yet; the iOS printer bridge is part of ut-docs#3071.
- **Not device-checked yet** — offline sale, rotation, long background,
  pairing on a real iPhone/iPad: ut-docs#3070.
