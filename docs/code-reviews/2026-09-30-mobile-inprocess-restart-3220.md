# Code review — "Restart now" restarts in-process on iOS/Android (ut-docs#3220)

- **Date:** 2026-09-30
- **Branch:** `fix/3220-mobile-inprocess-restart`
- **Author:** pipeline (lane:cloud-24, Opus 5.5). **Reviewer:** independent Fable subagent.
- **Card:** universaltill/ut-docs#3220 (p1 bug, complexity:medium)

## Problem

On the iOS/Android apps the till is a gomobile library inside the app
process. After joining a shop (`/api/{sync,setup}/pairing-restart`) or
staging a backup restore (`/api/backup/restart-now`) the "Restart now"
button called `procrestart.Restart()`, whose `syscall.Exec(os.Executable())`
can't work there (iOS forbids exec; on Android it would replace the app).
The failure was only logged, and the staged snapshot
(`db.ApplyPendingRestore`, run once at `app.Run` startup) was never applied.

## Change

- `internal/procrestart`: `SetRestarter` registers an in-process restarter
  used instead of re-exec (the `beforeRestart` plugin-stop hook still
  overlaps the flush delay); `Supported()` = build-tag re-exec support ||
  a registered restarter; `InProcess()` reports the latter.
  `reexec_unix.go` now builds for `!windows && !ios && !android`; new
  `reexec_mobile.go` (`ios || android`) has `supported = false`.
- `mobile`: `init()` registers `restartInProcess` — stop the server, start it
  again on the same data dir; `Start` reuses the persisted port, so the
  page's `/healthz` poll on the same address reloads into the restored till.
  While a restart runs, `IsRunning()` reports true and `Start`/`Stop` wait for
  it, so iOS `TillController.resume()` / Android `TillService` never race it.
- Backup restore (`/api/backup/restart-now`) uses the same `procrestart`
  path, so it is fixed by the same change.
- Help text (`web/help/*/backups.md`, `multitill.md`) already says the till
  restarts itself everywhere except Windows — now true on the apps; no edit.

## Tests

- `mobile`: `TestProcrestart_RestartsInProcessAndAppliesStagedRestore` —
  real server, a staged "joined shop" snapshot with a marker setting,
  `procrestart.Restart()`; asserts the server comes back on the same address
  with the restore applied and the marker present. **Red check:** with
  `restartInProcess` made a no-op it fails after 60s with
  `pending=true running=true`; passes with the fix.
  `TestRestartInProcess_NativeCallsWaitForIt`,
  `TestRestartInProcess_NotRunningErrors`.
- `procrestart`: restarter replaces re-exec (hook first, no exec), clearing
  it restores re-exec, restarter error survives; `platform_test.go` guard —
  the `supported = true` file never builds for ios/android/windows, and every
  GOOS gets exactly one `supported` declaration.
- Cross-compile vet: `GOOS=android GOARCH=arm64` and
  `GOOS=ios GOARCH=arm64 CGO_ENABLED=1`.

## Review findings (Fable) and triage

1. **major — restart not atomic vs native Start/Stop/IsRunning** (iOS resume
   probe during the gap could start a racing server and fail on the data-dir
   lock). **Fixed:** `restarting` channel; Start/Stop wait, IsRunning true;
   new test.
2. **minor — `Supported()` assertion vacuous on linux; a lost registration
   would re-exec the test binary.** **Fixed:** exported `InProcess()`, test
   asserts it before `Restart()`.
3. **minor — `pairing_wait.html` first probe at 2.5s can hit the old
   listener if a hardware-plugin stop takes >1s.** Pre-existing and shared
   with the re-exec path; no hardware plugins on the mobile apps. Not changed.
4. **nit — no log line on the in-process path.** **Fixed.**
5. **nit — GOOS expectation in tests ignored ios/android.** **Fixed**
   (`execSupportedGOOS`).
6. **nit, pre-existing — `TestRestartSchedulesDelayedReexecOfOwnExecutable`
   timing under `-race -count=3`.** Not caused by this diff; left.
7. **nit — persisted port grabbed in the gap → new address.** Very unlikely
   on-device; the native shell restarts on its next resume. Accepted.

## Gate

`gofmt -l` clean, `go build ./...`, `go test ./...` (all ok),
`go test -race` on the two packages, `golangci-lint run ./...` 0 issues,
guards data-access / core-neutral / i18n / kiosk-engine / help-topics /
help-drift / compliance-claims ok. `deadcode-baseline`: `SetRestarter`
(caller only in `mobile`, not a deadcode root — same as `SetAndroidBridge`,
`SetDeviceModel`) and `InProcess` (test-only) added to the baseline; the two
`internal/logging` hits it also prints locally are an artifact of this
container lacking the cgo desktop build (main's CI `build` is green).
`guard-gobind-skip` needs `gobind` (not installed locally; CI installs it).
