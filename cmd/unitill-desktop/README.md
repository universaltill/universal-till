# unitill-desktop — native webview shell

A desktop app that runs the Universal Till server and shows the UI in an
**embedded WebView window** (no browser chrome), so the till feels like a native
app. It launches the sibling `unitill-pos` binary, waits for it to come up, then
opens the window; closing the window stops the server.

This answers "an app that opens the web inside it, like a webview."

## Why it's a separate, tagged build

The WebView is **CGO** (system WebView2 on Windows, WKWebView on macOS,
WebKitGTK on Linux), which is incompatible with the pure-Go, cross-compiled
release binary. So it lives behind the `desktop` build tag:

- Default `go build ./...` / `go test ./...` (and CI) compile only `stub.go` —
  **no CGO, no WebView toolchain needed**. The release is completely unaffected.
- The real shell (`desktop.go`) compiles only with `-tags desktop`.

## Build

```sh
# from the repo root — needs CGO and the platform's WebView dev libraries
CGO_ENABLED=1 go build -tags desktop -o unitill-desktop ./cmd/unitill-desktop
```

- **macOS**: works out of the box (WebKit is part of the system).
- **Windows**: needs the WebView2 runtime (present on Windows 10/11).
- **Linux**: `sudo apt install libgtk-3-dev libwebkit2gtk-4.1-dev` (or 4.0).

## Run

Place `unitill-desktop`, the `unitill-pos` binary, and the `web/` folder in the
same directory, then launch `unitill-desktop`. It runs the till on
`127.0.0.1:8080` and opens the window.

## Linux startup gate (ut-docs#1093)

On Linux, the shell holds the window from opening until the machine is 60s
into its boot (`waitForSafeStartup` in `startup_gate_linux.go`) — a
mitigation for a WebKitGTK 2.52.6/Wayland defect where a window created too
early in boot renders with a permanently corrupt compositing surface. Only a
cold boot pays the cost; launched by hand on a running machine it's a no-op.

Tune or disable it with `UT_SHELL_MIN_UPTIME_SECONDS` (seconds; `0` disables
the gate entirely). Values outside `0..600` fall back to the 60s default
rather than being honoured, so a units-confusion typo can't hold the window
for hours or silently disable the gate. Windows and macOS use their own
platform web views and are unaffected — the gate is a no-op there.

## Status & follow-ups

Proof of concept — validated to build on macOS (arm64). Still to do:
- Cross-platform CI build + packaging (a `.app`/`.dmg` on macOS, bundle the exe
  in the NSIS installer on Windows) — needs per-OS runners.
- **Code signing / notarization** so it launches without OS warnings
  (Apple Developer account on macOS; a code-signing cert on Windows).
- Picking a free port when 8080 is taken; a proper app icon and menu.
