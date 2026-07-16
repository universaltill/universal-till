# Code review — real in-app auto-update for the macOS .app

Date: 2026-07-16
Branch: `feat/mac-app-auto-update`

## Why
The mac app's "update" was just a link to the download page — it navigated the
WebView away from the POS (no way back) and didn't actually update anything
(Farshid: "it should download the new version, close the app, install it, and
open it again").

## What
A real .app auto-update (`internal/selfupdate/macapp_darwin.go`, `applyMacApp`):
1. Fetch the latest release, find the `unitill-pos-<ver>-macOS-arm64.dmg`.
2. Download it, `hdiutil attach`, `ditto` the new **signed** `.app` out, detach.
3. `codesign --verify --deep --strict` the staged app (integrity gate — abort if
   it fails).
4. Launch a detached helper (`Setpgid`, `Process.Release`) that quits this app,
   replaces `/Applications/Universal Till.app` (backup + restore on failure),
   clears quarantine, and relaunches the new version.

Unlike the archive path it never swaps the inner signed binary (that breaks the
bundle; the archive binary is unsigned anyway) — it replaces the whole bundle
from the .dmg, which is validly ad-hoc-signed.

`Supported()` is true again for a `.app` (routes to `applyMacApp`); `Apply`
branches on `appBundlePath(exe)`. `macapp_other.go` stubs non-darwin so it still
cross-compiles. The status-bar button drops `window.confirm` (WebView ignores
it) for a two-click confirm and shows "Downloading update… / Restarting…".

## Verification
- Cross-builds darwin/linux/windows.
- The full mount→ditto→codesign flow validated against the real v0.2.5 .dmg
  (downloads, mounts, copies, signature verifies, version 0.2.5). The
  quit/replace/relaunch helper is not run in CI (destructive) but has a
  backup+restore fallback.
- Tests: `TestSupportedFor` (.app now true), `TestAppBundlePath`.

## Risk
The helper replaces the app bundle. It backs up to `.old` and restores on any
ditto failure, and only runs after the staged app passed a signature check.
First install of the version carrying this is still manual (.dmg); auto-update
applies from that version forward.

## Checks
`go build ./...` (darwin/linux/windows), `go test ./...`, guards — green.
