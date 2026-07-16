# Code review — mac .app update flow + regression tests

Date: 2026-07-16
Branch: `fix/app-update-and-tests`

## Bug: "update showed, clicked it, nothing happened" (mac .app)
`selfupdate.Supported()` returned true for a `.app` (its path isn't /usr or
/opt), so the status bar showed the self-update button. But a `.app` cannot
self-update: the release archive ships an UNSIGNED binary (won't run on Apple
Silicon) and swapping the binary breaks the signed bundle; also the webview's
`confirm()` returns nothing, so the click silently no-oped.

Fix:
- `supportedFor(exe, goos)` (testable) returns false for a `.app`
  (`.app/Contents/` in the exe path), .deb (/usr, /opt), and Windows. So the
  `.app` now shows the **download link** (reinstall the .dmg) instead of a dead
  self-update button. Self-update stays for portable .tar.gz installs.
- The download link dropped `target="_blank"` (webviews ignore it) so it
  navigates in place.

## Tests added (Farshid: "add tests for everything")
- `selfupdate.TestSupportedFor`: .app/.deb/windows → false, portable → true.
  Would have caught this bug.
- `pages.TestPluginStoreShowsCatalogForAnonymousTill`: renders the real
  `/plugins/store` page against a marketplace with plugins but an empty
  entitlements response — asserts the plugins still appear. This is the
  regression guard for the earlier "no plugins in the POS" bug (a rendered-page
  test, not a narrow unit).

## Note on the .app update UX
Because there's no Apple Developer cert (ad-hoc signed, manual install), a true
in-place self-update isn't safe. The correct flow for the .app is: the chip
shows "Update available v0.2.3" → download page → reinstall the .dmg. Portable
tar.gz (Linux/Pi) still self-updates in place.

## Checks
`go build ./...`, `go test ./...`, i18n + data-access guards — all green.
