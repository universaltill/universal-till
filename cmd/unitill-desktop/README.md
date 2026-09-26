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
and login session both reset on every restart/reboot. Windows (WebView2)
and macOS (`webkit_darwin.go`'s `WKWebView`) already persist cookies to
their own per-app data store by default and need no equivalent wiring.

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
  not the shell — so a browser opened by the WebView2 fallback survives. A
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
`procjob_windows_test.go` (kill the parent → its child dies) runs only on
Windows.

## Status & follow-ups

Proof of concept — validated to build on macOS (arm64). Still to do:
- Cross-platform CI build + packaging (a `.app`/`.dmg` on macOS, bundle the exe
  in the NSIS installer on Windows) — needs per-OS runners.
- **Code signing / notarization** so it launches without OS warnings
  (Apple Developer account on macOS; a code-signing cert on Windows).
- Picking a free port when 8080 is taken; a proper app icon and menu.
