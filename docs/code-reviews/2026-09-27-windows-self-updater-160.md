# Review — Windows in-app updater (ut-docs#160)

**Date:** 2026-09-27 · **Lane:** lane:cloud-24 · **Author:** Opus 5.5 · **Reviewer:** Fable (independent subagent, separate worktree)

## What shipped

A Windows installer install (a per-user `%LOCALAPPDATA%` install whose
directory holds the NSIS `uninstall.exe`) now updates itself. Before this,
the Windows till only showed a download link or "Update to vX needed".

- `internal/selfupdate/wininstaller.go`: `Apply`/`ApplyVersion`/`ApplyVersionWhenIdle` route here on Windows.
  The flow:
  1. Fetch the release (latest, or the exact tag a replica follows). It always comes from GitHub, never the main till (ADR-0011).
  2. Download `unitill-pos-setup-<v>.exe` into a fresh `paths.Data("updates")/attempt-*` folder.
  3. Check the SHA-256 against `checksums.txt`, then the Authenticode signature: status `Valid` and signer CN `TASK RUNNER TECHNOLOGY LTD`. Both fail closed.
  4. Wait for no open sale, write the restart marker, then, after the flush delay, check for an open sale again, stop the hardware plugins, and check once more.
  5. Re-check the SHA-256 and start the detached helper.
- The helper (`windowsUpdaterScript`, PowerShell, parameters only, never spliced text):
  1. Stops `unitill-desktop.exe`/`unitill-pos.exe` whose image is under the install dir. It uses WMI `ExecutablePath` and a CLM-safe prefix test.
  2. Confirms both processes are gone. If a lookup fails or a process survives, it installs nothing.
  3. Runs `Setup.exe /S /D=<dir>` and logs the exit code to `updates\updater.log`.
  4. Drops the marker if the install failed.
  5. Relaunches the shell (or the headless server) with cwd = the install dir.
- `winhelper_windows.go`: the helper runs from the System32 PowerShell by absolute path, with `CREATE_BREAKAWAY_FROM_JOB`. If this process sits in a kill-on-close job that refuses breakaway, the update is refused and the till keeps running. Without that refusal, the helper would die with the shell.
- `authenticode_windows.go`: `Get-AuthenticodeSignature`, with the path passed in an env var. `checkAuthenticodeResult` parses the result and is tested on every OS.
- `Supported()` on Windows requires a writable installer install. A portable zip keeps the download link.
- `windowsHandover` refuses a second update while one is handed over (applyMu is already released at that point).
- `installer.nsi` uninstall now deletes the marker.
- Manual: `web/help/{en,de,ar,fa,tr}/updates.md` steps 3–5.
- ut-docs: `architecture/packaging.md`.

## Findings (Fable) and what happened

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | Under #2760's kill-on-close job without BREAKAWAY_OK, the fallback "start inside the job" killed the helper along with the shell, so the till went down with nothing installed | **Fixed**: fails closed if the job is kill-on-close (`inKillOnCloseJob`). Contract noted on ut-docs#2760. |
| 2 | should-fix | Marker written before install; after a failed install the old build reported a false "restart pending" | **Fixed**: the helper takes `-Marker` and deletes it when the installer fails or never runs (script-shape test). |
| 3 | should-fix | The helper ran the installer even when it couldn't confirm the till had stopped (WMI failure, CLM, survivor), leaving a half install and a second shell | **Fixed**: re-queries after the stop loop and aborts on a lookup error or a survivor. The prefix test uses `Substring`/`-ieq`. |
| 4 | should-fix | A second Apply during the handover wiped the first one's files and `updater.log` | **Fixed**: per-attempt `MkdirTemp`, a kept `updater.log`, and the `windowsHandover` flag (tests). |
| 5 | nit (security) | Window between verify and run | **Fixed**: SHA-256 re-checked right before the helper starts (test). Same-user only either way. |
| 6 | nit | The O= fallback widened the signer check | **Fixed**: removed; CN only (test flipped). |
| 7 | nit | Plugins stay stopped after a failed helper start | **Fixed**: the Problem text says so. |
| 8 | nit | Uninstall leaves the marker | **Fixed** in `installer.nsi`. |
| 9 | nit | Checksum line lands after publish, so an early click gets "checksum not found" | **Accepted**: fails closed; the nightly/follow path retries. |
| 10 | nit | Help step 4 omitted portable-zip Windows replicas | **Fixed**, all five locales. |
| 11 | nit | Test goroutines sleep 1h; helper behaviour untested | **Accepted**: harmless. The PowerShell can only be exercised on Windows (see below). |

## Verification

- TDD: the test file was written first. Mutation-checked:
  - skip signature, skip checksum, accept any status, prefix-match signer, no idle wait, marker kept on failure: each fails its test;
  - after the review fixes: no re-check, no handover flag, no attempt cleanup: each fails its test.
  - Surviving mutant: `dirWritable` ignored. The container runs as root, so writability can't be denied here; this is the same known gap as `TestApplyReadOnlyDirIsUnsupportedAndLeavesBinary`.
- Commands:
  - `go test ./...` green.
  - `go test -race` green for `internal/selfupdate` (×3).
  - `internal/pages` green.
  - `GOOS=windows go build ./...` and `go vet` clean; `golangci-lint` 0 issues.
  - Guards green: i18n, help-drift, help-topics, docs-shots (pages edits are comment-only; surface hash refreshed), core-neutral, compliance, data-access.
- **Not verified:** a real Windows run (AC 6). No Windows host is reachable from a cloud lane, so the PowerShell helper, the Authenticode script and the process flags are unexecuted. Follow-up card (blocked:env, lane:local): update the Windows VM 0.x → next, manually and via auto-update, and confirm it comes back paired.
- No UI surface changed: the status-bar flow (Downloading… → Restarting… → poll `/api/update/status`) is unchanged, so there are no screenshots.

## CI follow-up

The first push was red on `desktop-shell` → `guard-deadcode-baseline.sh`. That analysis runs whole-program on Linux, and `checkAuthenticodeResult` was only called from `authenticode_windows.go`. Fixed without growing the baseline: the platform-neutral `verifyAuthenticode` (in `wininstaller.go`) calls it, and only `queryAuthenticode` is per-OS.

## Verdict

Safe to merge. The behaviour change is limited to Windows installer installs, and every failure before the helper starts keeps the running version. The device run is the remaining gate before the Windows update can be called proven.
