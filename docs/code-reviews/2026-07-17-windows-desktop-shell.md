# Code review — Windows desktop shell (app window like macOS)

**Date:** 2026-07-17
**Branch:** `feat/windows-desktop-shell`
**Ask (Farshid):** "in Windows it works but opens in a terminal and browser —
make it like macOS, an app with webview."

## What changed

The cross-platform shell (`cmd/unitill-desktop`, `webview_go` fallback → Edge
**WebView2** on Windows) already existed but was never built or shipped for
Windows. Now:

- **`.goreleaser.yaml`**: new `desktop-windows` build — CGO with mingw-w64
  (`CC/CXX=x86_64-w64-mingw32-*`), `-tags desktop`, `-H windowsgui` (the
  shell itself opens no console). Included in the Windows zip alongside
  `unitill-pos.exe`.
- **`release.yml`**: installs `gcc/g++-mingw-w64-x86-64` before goreleaser.
- **`installer.nsi`**: Start-menu/desktop shortcuts and the finish-page "run
  now" launch `unitill-desktop.exe`; uninstall removes it; DisplayIcon
  follows. `unitill-pos.exe` + `run-unitill.bat` stay for headless/portable
  use.
- **`childproc_windows.go`**: the spawned `unitill-pos.exe` (console
  subsystem) gets `HideWindow` + `CREATE_NO_WINDOW` so no terminal flashes.
  No-op hook on other platforms.
- **`webview_fallback.go` hardening**: `webview.New` returns nil when the
  system WebView is unavailable (a Windows box without the WebView2 runtime
  — rare on Win10/11 but possible). Instead of a nil-pointer crash the shell
  falls back to the default browser and exits when the server stops — i.e.
  exactly the old behaviour. (A WebView2 bootstrapper in the installer is a
  possible follow-up if anyone actually hits this.)

## Verification

- **Cross-compile proven locally** (brew mingw-w64): 9.5MB
  `unitill-desktop.exe` builds clean with `webview_go`'s WebView2 backend.
- **Full goreleaser snapshot build run locally**: windows zip contains both
  `unitill-pos.exe` and `unitill-desktop.exe`; all other artifacts intact.
- `goreleaser check`, workflow YAML parse, full test suite green.
- ⚠️ **Not run on real Windows** — no Windows machine in this environment.
  The mac shell's launch path is identical in structure, the Windows-specific
  parts (console hiding, WebView2) follow documented APIs, and the
  no-WebView2 fallback degrades to today's behaviour. Farshid tests the
  released installer on his Windows box; if the window doesn't appear, the
  browser fallback still leaves a working till.

## Notes

- Binaries remain unsigned → SmartScreen warning unchanged (documented,
  needs a code-signing cert eventually).
- The main-thread fix from the v0.2.11 mac crash (`runtime.LockOSThread` in
  init) applies to this shell too — webview_go on Windows also wants the
  main thread.
