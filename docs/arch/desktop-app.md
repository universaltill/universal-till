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

## Proper signing + notarization (removes the Gatekeeper prompt)

The build scripts are already wired for this — it becomes a matter of providing
credentials, not changing code. To open with a plain double-click and no
warning you need an **Apple Developer Program** membership ($99/year) and two
things:

1. **A Developer ID Application certificate.** In the Apple Developer portal
   (or Xcode → Settings → Accounts → Manage Certificates) create a
   *Developer ID Application* certificate. Export it from Keychain Access as a
   `.p12` (cert + private key) with an export password. The signing identity is
   its name, e.g. `Developer ID Application: Task Runner Technology LTD (TEAMID)`
   (`security find-identity -v -p codesigning` lists it).

2. **Notarization credentials.** Either an app-specific password
   (appleid.apple.com → Sign-In & Security → App-Specific Passwords) plus your
   Apple ID and 10-char Team ID, or an App Store Connect API key. Optionally
   store them once with
   `xcrun notarytool store-credentials <profile> --apple-id … --team-id … --password …`.

### Local build

```sh
export MACOS_SIGN_IDENTITY="Developer ID Application: … (TEAMID)"
# notarization (either the profile OR the three Apple-ID vars):
export MACOS_NOTARY_PROFILE="ut-notary"            # if you stored a profile
# or:
export MACOS_NOTARY_APPLE_ID="you@example.com" MACOS_NOTARY_TEAM_ID="TEAMID" MACOS_NOTARY_PASSWORD="abcd-efgh-ijkl-mnop"

packaging/macos/build-app.sh <version> dist/macos   # signs hardened-runtime + timestamp
packaging/macos/make-dmg.sh   <version> dist/macos   # notarizes + staples the .dmg
```

`build-app.sh` signs with the identity when `MACOS_SIGN_IDENTITY` is set
(hardened runtime + secure timestamp, as notarization requires) and ad-hoc
signs otherwise. `make-dmg.sh` notarizes + staples when the notary credentials
are present. With neither, the current free ad-hoc behavior is unchanged.

### CI (release pipeline)

The `macos-app` job in `.github/workflows/release.yml` signs + notarizes
automatically once these **repo secrets** exist (absent → ad-hoc, as today):

**Configured 2026-09-26 (ut-docs#2870).** Source of truth is the Key Vault
`kv-unitill-dev` (`apple-devid-p12`, `apple-devid-p12-password`,
`apple-asc-notary-key-p8`, `apple-asc-notary-key-id`, `apple-asc-issuer-id`,
`apple-team-id`); the GitHub secrets below are copies for the release job.

| Secret | What |
| --- | --- |
| `MACOS_CERT_P12` | base64 of the `.p12` (cert + key; key generated locally, never uploaded to Apple) |
| `MACOS_CERT_PASSWORD` | the `.p12` export password (random) |
| `MACOS_SIGN_IDENTITY` | `Developer ID Application: Task Runner Technology LTD (B45H898ZBK)` |
| `MACOS_NOTARY_KEY_P8` | App Store Connect **Team API key** `.p8` contents, role **Developer** |
| `MACOS_NOTARY_KEY_ID` | its Key ID |
| `MACOS_NOTARY_ISSUER_ID` | the Issuer ID |
| `MACOS_NOTARY_TEAM_ID` | 10-char Team ID |

The API key is preferred (`packaging/macos/notary-args.sh`): scoped, revocable
in App Store Connect, not a personal Apple ID password. It is written to a
0600 temp file for `notarytool --key` and deleted after the submit. The Apple
ID trio (`MACOS_NOTARY_APPLE_ID` + `MACOS_NOTARY_PASSWORD`) still works as a
fallback but is not set. After stapling, the build fails unless `stapler
validate` and `spctl --assess` accept the `.dmg`.

**Rotate:** revoke the key in App Store Connect → Users and Access →
Integrations, create a new one (role Developer), update both Key Vault and the
GitHub secrets. The Developer ID certificate expires 2031-09-17.

The job imports the cert into a throwaway keychain, then the scripts pick up
the env. Windows binaries are Authenticode-signed with Azure Artifact Signing
instead (no certificate secret — `packaging/windows/sign-exe.sh`, ut-docs#2480,
#2610).

## Follow-ups

- **Intel (amd64)** macs are not built (arm64 only), matching the tar/deb.
- The same `webview_go` shell can produce a Windows app later; the pure-Go
  server + NSIS installer already covers Windows.
