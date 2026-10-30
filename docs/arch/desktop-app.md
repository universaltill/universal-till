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

### Hardened runtime and entitlements (ut-docs#5023)

A Developer ID build (`MACOS_SIGN_IDENTITY` set) signs with the hardened
runtime, which forbids making memory executable. `unitill-pos` compiles WASM
plugins to machine code at runtime (wazero: `mmap` + `mprotect(RX)`, not
`MAP_JIT`), so it carries `com.apple.security.cs.allow-unsigned-executable-memory`
from `packaging/macos/entitlements.plist` — and only that one;
`allow-jit` alone is not enough. `unitill-desktop` (WKWebView) needs none.
Inner binaries are signed first, then the bundle **without `--deep`**
(`--deep` re-signs nested code and drops its entitlements); the script fails
the build if the entitlement is missing. An ad-hoc build has no hardened
runtime, so it runs even without the entitlement: only a hardened build proves
the fix.

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
0600 temp file for `notarytool --key` and deleted once notarization ends. The Apple
ID trio (`MACOS_NOTARY_APPLE_ID` + `MACOS_NOTARY_PASSWORD`) still works as a
fallback but is not set. After stapling, the build fails unless `stapler
validate` and `spctl --assess` accept the `.dmg`.

**Notarization never holds a release (ut-docs#2917).** `make-dmg.sh` submits
without `--wait`, writes the submission id to the job summary (check it with
`xcrun notarytool info <id> …`), then waits with `notarytool wait --timeout`
(`NOTARY_TIMEOUT`, 45m in CI; job `timeout-minutes: 90`). Only status
`Accepted` passes. `publish-release` does not need `macos-app`: Linux, Windows
and Android go live first, and `macos-dmg-attach` attaches the `.dmg` (and
folds its `checksums.txt` line, then re-verifies every asset) after publish,
only if `macos-app` succeeded. On a timeout or rejection `macos-app` fails
loudly and nothing is attached — never an un-notarized `.dmg`. Recover with
"Re-run failed jobs" on that run (never cancel a release); that builds and
submits a **new** `.dmg` — the recorded id is only for `notarytool info/log`
diagnosis. Mac tills whose nightly auto-update falls between publish and the
attach retry the next night (the updater fails closed on a missing `.dmg`).

If `macos-app` is cancelled because GitHub never gave it a runner ("The job
was not acquired by Runner of type hosted", ut-docs#5175),
`.github/workflows/release-runner-retry.yml` re-runs it automatically
(`scripts/ci/release-runner-retry.sh`): only when `publish-release`
succeeded (post-publish case) and every failed job is runner-starved, at most
once in practice (bounded by `MAX_ATTEMPTS=3`). Real failures are never
retried automatically.

A starved job *before* publish (ut-docs#5205), e.g. `linux-shells` on the
hosted arm pool, skips `goreleaser` and `publish-release`, and the jobs that
wait for the release (`windows-installer`, `macos-app`, `android-app`,
`verify-versions`) are skipped too instead of failing. The runner retry then
re-runs the failed jobs on attempt 1 only (`PREPUBLISH_MAX_ATTEMPTS=2`: a
re-run started with `GITHUB_TOKEN` may not fire another `workflow_run`), and
`cleanup-orphaned-tag` keeps the tag and draft for it. It asks the same script
(`release-runner-retry.sh keep-tag`), so both agree; on attempt 2, with any
real failure, or on any API error the tag is deleted as before and a fresh
release is dispatched. `release-runner-retry_test.sh` pins the rule and the
`release.yml` wiring. Every job that gates `publish-release` runs on Linux
(`verify-versions` moved off `macos-14` in ut-docs#5200, pinned by
`release-macos-nonblocking_test.sh`), so a starved macOS pool can only delay
the .dmg, never the release.

A starved `goreleaser` or `android-app` (cancelled, ut-docs#5209) no longer
cascades: `windows-installer`, `macos-app`, `android-app` and `verify-versions`
skip when `goreleaser` was cancelled, and `verify-versions` also when
`android-app` was. A goreleaser *failure* still runs them. The retry then sees
only the one starved job and re-runs it.

Before a pre-publish re-run the retry checks that the release tag still
exists. `cleanup-orphaned-tag` runs seconds after the cancel and can delete the
tag before the starvation annotation is readable, while the retry reads it
fine later. The retry gets the tag from `head_branch` (tag push) or from
`cleanup-orphaned-tag`'s keep warning (dispatch). If the tag is gone or
cannot be confirmed, it does not re-run and prints a warning: for a dispatch,
dispatch a fresh release and delete a leftover tag by hand if there is one;
for a tag push, use "Re-run failed jobs" by hand if the tag exists. A starved
`prepare` never made a tag, so that re-run needs no check. If the retry never
comes (the `workflow_run` was not delivered, the retry job was itself starved,
the re-run request failed or it was refused), `release-orphan-tag-sweep.yml`
cleans up: it runs hourly and, for a `workflow_dispatch` Release run that
failed at least 2 hours ago (within 72 hours) whose latest
`cleanup-orphaned-tag` carries the keep warning and whose `publish-release`
did not succeed, deletes the draft release(s) and then the tag, so the version
is not burned (ut-docs#5211). Only the newest Release run (any event) is ever eligible, because a re-dispatch
recreates the same tag name and its in-flight draft; an orphan from an older run
is not swept (its version is already skipped) and needs a manual delete if
wanted. Before deleting it also checks the tag is the annotated one that run's
`prepare` created (object sha equal to the run's head sha, tagger
`github-actions[bot]`, message `Release <tag>`) and that no published release
exists for it (two independent lookups). It never touches a pushed tag, a tag whose
release is published, or a run still in progress, and does nothing if the
releases lookup fails. Its job holds only `contents: write` (delete the draft
and tag ref), `actions: read` and `checks: read`. Dispatch a fresh release
afterwards.

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
