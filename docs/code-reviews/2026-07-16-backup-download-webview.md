# Code review — backup "download" works in the desktop WebView

Date: 2026-07-16
Branch: `fix/backup-download-webview`

## Bug
"Download DB backup" did nothing in the mac app. The row linked to
`/api/backup/download/{name}` which serves the file with
`Content-Disposition: attachment` — but WKWebView (the desktop shell) does not
download attachments, so the link silently no-oped. (Same webview class of bug
as the update link.)

## Fix
- New `POST /api/backup/save-copy/{name}` (manager-gated, audited) copies the
  backup into the user's Downloads folder and reports the path — an htmx POST,
  so it works in the WebView and in a browser. The backup file is already on
  this machine, so a copy-to-Downloads is the right desktop action.
- Settings backup row: the "Download" control is now this POST button
  (relabelled "Save to Downloads"); result shown in `#backup-dl-msg`.
- `copyBackupTo` split out (testable without touching the real ~/Downloads);
  `TestCopyBackupTo` guards it. The direct-download route is kept for LAN
  browsers.
- i18n `settings.backup.saved_to` / `save_failed` in all four locales.

## Note
For a till accessed from another device over the LAN, this saves to the till's
own Downloads (the direct-download route still serves remote browsers).

## Checks
`go build ./...`, `go test ./...`, i18n + data-access guards — green.
