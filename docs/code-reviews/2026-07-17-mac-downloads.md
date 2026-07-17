# Code review — mac app: generic downloads (WKDownloadDelegate) (2026-07-17)

Branch `feat/mac-downloads`. Queue item: arbitrary downloads inside the mac
app (previously only specific save-to-Downloads endpoints worked; generic
download links did nothing in WKWebView).

## What changed (cmd/unitill-desktop/webkit_darwin.go, ObjC preamble)
- `decidePolicyForNavigationResponse`: responses with
  `Content-Disposition: attachment` or an MIME type the view can't display
  become WKNavigationResponsePolicyDownload; `shouldPerformDownload`
  navigation actions (`<a download>`) likewise.
- `UTDelegate` is now also the `WKDownloadDelegate`: destination is
  ~/Downloads with browser-style "name (2).ext" dedupe; finish/fail logged.
- Everything guarded `@available(macOS 11.3, *)` — older macOS keeps the
  previous behavior (server-side save endpoints still exist as fallback).

Verified: desktop-tagged build compiles; full till build + pages suite
green. Real click-through (CSV export from the app) rides the next dmg
launch-test.
