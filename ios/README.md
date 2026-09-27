# Universal Till for iPhone and iPad

The iOS/iPadOS till app (ADR-0023 §1, ut-docs#3046). Like the Android app
it is **not a rewrite**: the same Go till server every other platform runs
is bound into the app with `gomobile bind` (`../mobile`) and started
in-process; a small SwiftUI app shows it in a `WKWebView`. Everything the
operator sees and does is the server's web UI.

## Build (a Mac with Xcode)

```bash
go install golang.org/x/mobile/cmd/gomobile@latest golang.org/x/mobile/cmd/gobind@latest
brew install xcodegen

# from the repo root: the Go server as an iOS framework
gomobile bind -target=ios,iossimulator -o ios/build/Mobile.xcframework ./mobile

cd ios
xcodegen generate            # writes UniversalTill.xcodeproj from project.yml
open UniversalTill.xcodeproj # run on a simulator; a device needs your signing team
```

`UniversalTill.xcodeproj`, `UniversalTill/Info.plist` and `build/` are
generated and git-ignored; `project.yml` is the source of truth.

CI (`.github/workflows/ios-ci.yml`) does the same on a GitHub macOS runner
for every change under `ios/**` or `mobile/**`, unsigned, then runs
`scripts/ci/ios-sim-smoke.sh`: it launches the app on an iPhone and an iPad
simulator, waits for the till server inside it to answer `/healthz`,
backgrounds and resumes it, and uploads screenshots as the
`ios-simulator-screenshots` artifact.

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
- **No signing, no TestFlight yet** — ut-docs#3069 (internal testing only
  until launch). No app icon yet either; TestFlight needs one.
- **Android-only bridges have no iOS counterpart yet** — file downloads
  (exports), the bug-report screenshot, the self-order kiosk lock (Guided
  Access) and Bluetooth printers/scanners: ut-docs#3071.
- **Not device-checked yet** — offline sale, rotation, long background,
  pairing on a real iPhone/iPad: ut-docs#3070.
