# macOS desktop app (WebView)

Universal Till also ships as a real macOS application — `Universal Till.app` —
that runs the POS and shows it in an embedded WebView window (WKWebView), with
no Terminal and no browser chrome. This is the friendly alternative to the
portable `.tar.gz` (unzip + double-click a `.command`).

## How it works

- `cmd/unitill-desktop` (build tag `desktop`, CGO + `webview/webview_go`) is a
  tiny shell: it launches the sibling `unitill-pos` server (`UT_OPEN_BROWSER=0`),
  waits for it to accept connections, opens a WebView at `http://127.0.0.1:8080`,
  and stops the server when the window closes.
- Bundle layout (`packaging/macos/build-app.sh`):
  - `Contents/MacOS/unitill-desktop` — the app executable (Info.plist entry)
  - `Contents/MacOS/unitill-pos` — the pure-Go server
  - `Contents/Resources/web/` — templates + assets (a *resource*, so codesign
    accepts the bundle; the shell points the server's working dir here)
  - `Contents/Resources/AppIcon.icns` — generated from the logo SVG
- Data (DB, backups, plugins) is **not** written inside the read-only bundle —
  it uses the per-user dir `~/Library/Application Support/UniversalTill`
  (`internal/paths`), so it survives app replacement.

## Signing & Gatekeeper (why it opens without a paid cert)

The bundle is **ad-hoc codesigned** (`codesign --sign -`). This is required:
Apple Silicon kills unsigned arm64 binaries on launch. Ad-hoc signing is *not*
Apple notarization, so a **downloaded** copy is quarantined and Gatekeeper
still asks the user to confirm the first launch. The `.dmg`
(`packaging/macos/make-dmg.sh`) makes that easy:

- drag `Universal Till.app` to the `Applications` symlink, then either
- **right-click the app → Open** once (the standard unsigned-app path), or
- double-click **“Open Universal Till.command”** in the DMG, which clears the
  quarantine attribute and launches it.

## Building

```sh
packaging/macos/build-app.sh <version> dist/macos   # → dist/macos/Universal Till.app
packaging/macos/make-dmg.sh   <version> dist/macos   # → dist/macos/unitill-pos-<version>-macOS-arm64.dmg
```

CI builds and attaches the `.dmg` on every `v*` tag (the `macos-app` job in
`.github/workflows/release.yml`, on a `macos-14` Apple Silicon runner). The
download page offers it as the primary macOS download.

## Follow-ups

- **Notarization** (removes the Gatekeeper prompt entirely) needs a paid Apple
  Developer account: a Developer ID Application cert to sign with, then
  `xcrun notarytool submit` + `xcrun stapler staple`. Swap the ad-hoc
  `codesign --sign -` for the real identity and add the notarize step.
- **Intel (amd64)** macs are not built (arm64 only), matching the tar/deb.
- The same `webview_go` shell can produce a Windows app later; the pure-Go
  server + NSIS installer already covers Windows.
