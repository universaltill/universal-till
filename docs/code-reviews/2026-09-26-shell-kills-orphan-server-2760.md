# Review — Windows: the till server no longer outlives a crashed shell; the installer stops a running till (ut-docs#2760)

- **Date:** 2026-09-26
- **Branch:** `fix/2760-shell-kills-orphan-server`
- **Author lane:** `lane:cloud-54` (built on Opus 5.5)
- **Reviewer:** independent subagent on Fable (a different model), one round

## What shipped

- `internal/procjob` (new, cgo-free): `KillWithParent(p)` puts the spawned
  `unitill-pos.exe` in a Windows Job object with
  `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`. The handle is deliberately leaked so
  the OS closes it, and kills the server, when the shell exits for any
  reason, crash included. Only the child goes in the job, never the shell,
  so a browser opened by #2761's fallback survives. No-op elsewhere.
- `cmd/unitill-desktop/desktop.go`: calls it after `cmd.Start()` (a warning
  in `desktop.log` on failure, never fatal). The 10 s dial loop is now
  `waitForChild` (`child_wait.go`, pure logic). It stops as soon as the child
  exits and logs the exit status. On macOS the child is not reaped (the
  review's PID-reuse point), so macOS behaviour is unchanged.
- `packaging/windows/installer.nsi`: a `StopRunningTill` macro at the start
  of both the install and the uninstall section. It stops
  `unitill-desktop.exe`, then `unitill-pos.exe`, but only copies whose
  `Win32_Process.ExecutablePath` is under `$INSTDIR`. The path is passed via
  the `UT_STOP_DIR` env var. It waits up to 15 s for each, logs what it did,
  and on any failure logs "Could not check…" and continues.
- `ci.yml` `build` job: `GOOS=windows go vet ./internal/procjob/ ./internal/db/`.
  Nothing on a PR compiled Windows-only Go before.
- Docs: a README section. The manual `updates` step 5 says the Windows
  installer closes a running till first (en/de/ar/fa/tr, one sentence, no
  structural change).

Out of scope: #2761's WebView2 fallback (universal-till#1414, which is what
made the relaunch show nothing). This diff touches none of its files except
a separate README section.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Blocker | The installer is 32-bit, so `nsExec` starts 32-bit PowerShell. Its `Get-Process` returns a null `Path` for 64-bit processes, so the stop filter matched nothing on every 64-bit Windows, and File failed exactly as in the issue. | **Fixed:** processes are found via WMI (`Get-CimInstance Win32_Process`, `ExecutablePath`), which works regardless of bitness. The reason is in a comment in the macro. |
| 2 | Minor | macOS: reaping the child with `cmd.Wait()` lets its PID be reused before the Cocoa close handler SIGTERMs it by PID. | **Fixed:** macOS doesn't reap (a nil exit channel), so the old behaviour is kept there. |
| 3 | Minor | `exit 0` hid script errors behind the success path. | **Fixed:** `try { … } catch { Write-Output $_; exit 1 }`, and the install log says "Could not check…". |
| 4 | Nit | `$INSTDIR\` doubles the backslash for a drive-root install. | **Fixed:** `$INSTDIR` is passed bare and normalised with `TrimEnd('\') + '\'`. |
| 5 | Nit | After an early exit the shell still opens a window on a dead port (now logged). | **Accepted:** same as before, only faster and logged. A native "already running" dialog would be a separate improvement. |

The reviewer checked and found these OK: the NSIS/System-plugin quoting,
the StrCmp offsets and the uninstaller's `$INSTDIR`. On the job itself: the
OpenProcess rights, no GC close of the handle, no PID reuse between Start
and Assign, and Win7 nested-job failure is caught and logged. Also: hardware
plugins die with the server (intended), the server opens no browser, and
there is no in-app updater on Windows. No double Wait; the tests can't
false-pass; the translations are accurate.

## Verified beyond unit tests

- **Job object, under Wine (wine64):** `procjob_windows_test.go` spawns a
  "shell", which spawns a "server", ties it, and then gets TerminateProcess'd
  with no cleanup. With the fix, the server dies (PASS, 0.34 s). With
  `killWithParent` stubbed to `return nil`, the test fails with "server still
  running 10s after its shell was killed". So the red/green is real.
- **Installer script:** `makensis` compiles it with 0 warnings. The exact
  `-Command` body NSIS passes (`$$`→`$`, checked to contain no `"`) was run
  in PowerShell 7 against a mocked `Get-CimInstance`:
  - it stopped a real process whose path is under the install dir, with an
    apostrophe in the user name and a case difference;
  - it left a sibling `…\Universal Till Other\` process running;
  - it skipped a null path;
  - with the real cmdlet missing, it took the catch path and exited 1.
- The 32-bit installer itself could not run here (no 32-bit Wine).
- **Gate:** `gofmt`, `go build ./...`, `go test ./...` (a one-off
  `internal/pages` failure was my guard loop rewriting `tr.json` at the same
  time; the package re-ran green on its own), `golangci-lint` (root, desktop
  tag, and GOOS=windows on procjob), `go vet -tags desktop`, the deadcode
  baseline, and the build-job guards (68/69; `guard-shellcheck-version`
  needs the runner's shellcheck binary; no shell scripts changed).

## Still needs a human (Windows VM)

On the Windows VM:
1. Install, start the till, and `taskkill /F /IM unitill-desktop.exe`.
   `unitill-pos.exe` must disappear.
2. With the till running, re-run the installer. The details show
   "Stopping unitill-…", and the install succeeds.

## Verdict

Safe to merge.
