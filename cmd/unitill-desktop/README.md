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
platform web views and are unaffected — the gate is a no-op there. Disabling
this gate does not also disable the attach-probe retry below — see
ut-docs#1278 in the next section.

## Linux WebKit cookie persistence (ut-docs#1233)

On Linux, the shell points the default `WebKitWebContext`'s cookie
manager at a persistent SQLite file under
`~/.local/share/universal-till/webkit/cookies.sqlite` (honoring
`$XDG_DATA_HOME`, see `webkit_datadir_linux.go`/`webkit_linux.go`) —
without it, `webview_go`'s GTK/WebKit2 backend keeps an in-memory-only
cookie jar that dies with the process, so the language choice (`ut_lang`)
and login session both reset on every restart/reboot. macOS
(`webkit_darwin.go`'s `WKWebView`) persists cookies to its own per-app data
store by default and needs no equivalent wiring. Windows (WebView2)
persists them too; since ut-docs#2761 its user data folder is pinned to
`<data dir>\webview2` (`UT_DATA_DIR`, default
`%LOCALAPPDATA%\UniversalTill`) — see the next section.

## Windows WebView2 failure → browser fallback (ut-docs#2761)

If WebView2 cannot start — no runtime installed, or stale
`msedgewebview2.exe` processes still holding the user data folder after a
runtime self-update (a reboot clears them) — the shell no longer crashes
(`0xc0000005` in `webview_navigate`). It opens the till in the default
browser instead and keeps serving until the till stops, and `desktop.log`
records why: the WebView2 `HRESULT` and the installed runtime version
(logged at every start as `WebView2 runtime version=…`).

This needed patches to the vendored `internal/thirdparty/webview_go`
(each marked `universal-till patch`): `webview.NewWindow` returns `nil` on
failure (upstream never did, so `showWindow`'s fallback was unreachable);
the Windows engine fails creation when WebView2 embedding fails, and gives
up instead of waiting forever on `ERROR_INVALID_STATE`; `LastInitError`
exposes the `HRESULT` (`ERROR_FILE_NOT_FOUND` = no runtime installed).
The user data folder is set through `WEBVIEW2_USER_DATA_FOLDER`
(`webview2_windows.go`), which the patched `embed()` reads itself — the
library's built-in loader otherwise ignores it; an operator-set value
wins.

## Attach-vs-spawn cold-boot race (ut-docs#1199)

Before deciding whether to attach to an already-running `.deb`/systemd
service or spawn its own `unitill-pos` child, the shell probes `:8080`'s
`/healthz` (`tillAlreadyRunning`, `desktop.go`). On Linux that probe now
**retries** (`waitForAttach`, `attach_gate.go`) across the same window the
startup gate above already holds the window open for, instead of deciding
from a single probe — a systemd service that hasn't finished binding
`:8080` yet (routine on a cold boot; the whole service start time is the
race window) used to lose that single probe every time, so the shell spun
up a *second* server as the desktop user instead of attaching to the real
one. Consequences of that: the on-screen till traded against the spawned
child's own SQLite file instead of the service's, and in-app update
honestly reported unsupported, because the process actually serving the UI
couldn't write the service's install directory.

On the attach path the retry costs nothing: the startup gate above was
going to hold the window shut until the same instant regardless. The one
case that does pay is a cold boot where the retry runs its whole window and
still finds nothing — the `unitill-pos` child is then spawned *after* that
window instead of at the first probe, so its start-up no longer overlaps
the gate's hold and the till appears a few seconds later than it used to (a
tarball install, or a `.deb` whose service is down). That is inherent, not
an oversight: spawning in parallel with the retry is exactly the two-server
split this fixes.

The retry only runs on Linux; other platforms/topologies (macOS, Windows, a
warm/manual launch already past the gate) still decide from one probe, same
as always. While the startup gate is active, the retry window is derived
from its duration — the two windows coincide, which is what makes the
retry free on the attach path above. **When the gate is disabled
(`UT_SHELL_MIN_UPTIME_SECONDS=0`), the retry keeps its own independent 15s
floor instead (ut-docs#1278)** — a machine that doesn't need the WebKitGTK
render mitigation still gets a real chance to see the service come up,
rather than reverting to a single probe and losing this race. See
`attach_gate.go`'s doc comment for the exact retry-vs-give-up logic and its
tests.

**If an install was already bitten by the race before this fix** (two
`unitill-pos` processes, one on `:8080` as the desktop user with its own
`~/.local/share/universal-till/unitill-pos.db`, one as `pos` under
`/opt/unitill/data` never actually serving): stop both processes, decide
which database holds the real recent activity (the desktop-user one is
whatever was rung up on-screen since the split began), and either restart
clean on the systemd service's own DB (accepting the desktop-user copy's
sales are lost) or manually reconcile the two before restarting — there is
no automated merge tool for this, it's a one-off recovery, not a shipped
feature.

## Server outlives a crashed shell (Windows, ut-docs#2760)

When the shell died without running its deferred `cmd.Process.Kill()` — the
WebView2 crash of ut-docs#2761, or a `taskkill` — the `unitill-pos.exe` it had
spawned kept running. The orphan held the data directory, so a relaunch
either attached to it or started a second server that exited with
`db.ErrDataDirLocked`, and the installer failed with "Error opening file for
writing" on `unitill-pos.exe`. Three changes close this:

- **Kill-on-close job** (`internal/procjob`): right after `cmd.Start()` the
  child goes into a Windows Job object with
  `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`. The shell never closes the handle;
  the OS does when the shell exits for any reason, and kills the server (and
  the hardware plugins it started) with it. Only the child is in the job —
  not the shell — so a browser opened by the WebView2 fallback survives. The
  job also sets `JOB_OBJECT_LIMIT_BREAKAWAY_OK`: the in-app updater's helper
  (`internal/selfupdate`, ut-docs#160) starts with
  `CREATE_BREAKAWAY_FROM_JOB` so it outlives the shell and server it stops,
  and refuses to start inside a kill-on-close job without it. A
  failure to create/assign the job is a warning in `desktop.log`, never
  fatal. No-op on other platforms.
- **Early exit is logged** (`child_wait.go`): the ~10s wait for the server
  stops as soon as the child exits and `desktop.log` records its exit
  status, instead of polling a dead port and recording nothing. Not on
  macOS: reaping the child there would let its PID be reused before the
  window-close handler SIGTERMs it.
- **Installer/uninstaller** (`packaging/windows/installer.nsi`,
  `StopRunningTill`): before extracting or deleting, stop any
  `unitill-desktop.exe`/`unitill-pos.exe` whose image lives under
  `$INSTDIR` (PowerShell + WMI `Win32_Process.ExecutablePath` — the 32-bit
  installer's PowerShell can't read a 64-bit process's `Get-Process` path —
  path passed via an env var), wait for them to exit, and say so in the
  install log.

`GOOS=windows go vet ./internal/procjob/` runs in `ci.yml`'s `build` job;
`procjob_windows_test.go` (kill the parent → its child dies; a member can
start a process with `CREATE_BREAKAWAY_FROM_JOB`) runs only on Windows —
both pass under Wine and fail without the respective flag.

## Linux self-healing: upgrade re-exec and WebKit recovery (ut-docs#2991)

Field report (Pi 5, `.deb`, attach mode): the shell kept running through an
apt upgrade that replaced `/opt/unitill/bin/unitill-desktop` on disk
(`/proc/<pid>/exe -> … (deleted)`), and later its WebKit web process died,
leaving WebKit's built-in "WebKit encountered an internal error" page on
screen — a dead end on a till with no browser chrome. Two Linux-only fixes:

- **Re-exec after an upgrade.** Every 30s the shell stats its own
  executable (`watchOwnBinary`, `exe_watch.go`; glue in
  `exe_reexec_linux.go`). Once the path names a different file than the one
  running (baseline taken from `/proc/self/exe`) and that new file looked
  identical on two consecutive checks — so a half-written file is never
  exec'd — it logs the reason and `exec`s the new binary with the same
  arguments and environment. In spawn mode it first stops the `unitill-pos`
  it started (SIGTERM, SIGKILL after 10s) so nothing is orphaned; the fresh
  shell then attaches/spawns as on any launch — typically 30–90s after dpkg
  finishes. A replaced file that isn't executable is logged and the old
  shell keeps running; an exec that still fails after the server was
  stopped restarts the running image via `/proc/self/exe` instead, so a
  fresh server is spawned. `internal/selfupdate` never replaces this
  binary (only `unitill-pos` + `web/`, after which the web UI reloads
  itself), so in practice this is the apt/`.deb` upgrade path.
- **WebKit crash / load-failure recovery.** `web-process-terminated`
  reloads the page the view was on; a main-frame `load-failed` on the
  till's own origin (scheme+host+port) reloads the failing URL and
  suppresses WebKit's error page; cancellations (`WebKitNetworkError` 302),
  policy interruptions such as downloads (`WebKitPolicyError` 102) and
  `WebKitPluginError` 204 are left alone. Retries back off 0s, 1s, 2s … 30s
  and continue at 30s until the till answers again (a server that is down
  for minutes heals by itself); a successful commit resets the backoff.
  Policy in `webkit_recovery.go` (untagged, tested), wiring in
  `webkit_recovery_linux.go` / `webkit_recovery_cgo_linux.go`. Every attempt
  logs one `webkit recovery:` line to `desktop.log`.

While the till is unreachable the window shows the last page or a blank
view, not an error screen. A proper recovery page with **Reload** /
**Return to desktop** is a follow-up card. macOS and Windows are unchanged.

## Status & follow-ups

Proof of concept — validated to build on macOS (arm64). Still to do:
- Cross-platform CI build + packaging (a `.app`/`.dmg` on macOS, bundle the exe
  in the NSIS installer on Windows) — needs per-OS runners.
- **Code signing / notarization** so it launches without OS warnings
  (Apple Developer account on macOS; a code-signing cert on Windows).
- Picking a free port when 8080 is taken; a proper app icon and menu.
