# Code review — native macOS WKWebView shell (capable desktop app)

Date: 2026-07-16
Branch: `feat/native-webview-shell`

## Why
The mac app used `webview_go`, a minimal WKWebView wrapper with no clipboard,
camera, file pickers, popups or JS dialogs — the root cause of six field
reports (copy/paste, AI camera, import, receipt-logo upload, "popups not
opening", CSV download).

## What
- `cmd/unitill-desktop/webkit_darwin.go`: a native WKWebView shell (Cgo +
  Objective-C in the cgo preamble, so it only compiles on darwin) implementing:
  - **Edit menu** (Cut/Copy/Paste/Select-All/Undo/Redo) → clipboard + shortcuts.
  - **WKUIDelegate `runOpenPanel`** → native file picker (import CSV, logo upload).
  - **`requestMediaCapturePermission`** → grants camera/mic (AI scanning).
  - **`createWebView`** → loads `window.open`/`target=_blank` in place (popups).
  - **JS `alert()`/`confirm()`** → native panels.
  - Stops the POS server (SIGTERM by PID) when the window closes, since closing
    calls `[NSApp terminate:]` and skips Go defers.
- `desktop.go`: platform-agnostic launcher; delegates the window to
  `showWindow` (native on darwin, `webview_go` on `webview_fallback.go` for
  Linux/Windows). Also prefers a **stable :8080** so the till is reachable from
  a normal browser too.
- Info.plist: `NSCameraUsageDescription` / `NSMicrophoneUsageDescription`
  (macOS blocks camera without them).

## Verification
- `go build ./...` (no tag, CI path) OK; `go build -tags desktop ./cmd/unitill-desktop`
  compiles the Cgo/ObjC shell (darwin) and the webview_go fallback path.
- Built the full `.app`, launched it: the server came up on :8080 (HTTP 200),
  window opened, clean quit. GUI interactions (copy/paste, camera, pickers) need
  a human to exercise but use standard, well-supported WebKit delegate APIs.

## Not covered here (follow-up)
- **CSV export download**: no WKDownloadDelegate yet — do the server-side
  save-to-Downloads (like the backup fix) so export works in the app.
- Printer-type-aware printing, catalog variants/barcodes, shared settings — see
  docs/arch/field-issues-2026-07-16.md.

## Checks
Normal + desktop builds green; full .app launches and serves.
