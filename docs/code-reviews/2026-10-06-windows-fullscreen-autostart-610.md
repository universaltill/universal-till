# Review: Windows fullscreen/kiosk window mode + Startup-folder autostart (ut-docs#610)

Date: 2026-10-06 · Lane: lane:local · Built by: Claude Opus 5.5 · Reviewed by: Claude Fable 5.1 (independent subagent, separate worktree)

## What shipped

- **Window modes on Windows.** `win32_styles.go` holds the pure helpers and is untagged, so plain `go test` covers it. `win32window_windows.go` uses user32 via x/sys/windows, is tagged `windows` and uses no cgo.
  - Borderless fullscreen works as in Chromium: save the style and placement, strip the caption and frame, size the window to the monitor rect. The taskbar stays hidden while the till has focus.
  - Leaving fullscreen restores the saved frame and fits it into the window's own monitor work area, in screen coordinates. A shell launched straight into fullscreen gets the centred default size instead. Maximize happens after that fit, so the caption's Restore button has a sane rect.
  - `window_mode_windows.go` claims ADR-0064 `control=live` (`shellAppliesWindowMode = true`).
- **Autostart.** `shelllink_windows.go` and `autostart_windows.go` manage a Startup-folder `Universal Till.lnk`, written through COM IShellLinkW/IPersistFile on its own locked STA thread. The shortcut is reconciled on every launch, following the design spec `ut-docs/reference/desktop-kiosk-overlay-macos-windows.md`.
- **Ack race in `watchShellMode`, a cross-platform bug found on the VM.** The next long poll used to leave before the apply finished. It carried the old `applied=`, so the ack missed the server's 3 s exit-to-os window: the endpoint returned 503 although the window came back. The loop now waits up to `shellApplyAckWait` (2 s) for `done()`. The contract test pins that wait below `common.ShellExitAckTimeout`.
- **Installer, fresh installs only.** It creates the Startup shortcut, reads it back with `IfFileExists` for the `--autostart-staged` flag, then runs `provision-desktop-kiosk-defaults --trigger=windows-installer`. The uninstaller removes the shortcut.
- **CI.**
  - A mingw cross-build of the desktop shell on PRs. Before this, the `desktop && windows` files compiled only at release.
  - A `windows-latest` job running the Win32 and `.lnk` tests on real Windows.
  - `cmd/unitill-desktop` added to the GOOS=windows vet.
- **Docs.** The help topic `display.md` (en, de, tr, ar, fa) and `settings.display.window_mode_pending_note` (en, ar, fa, tr) now say that Windows applies the mode immediately. The de and es packs get their own PRs.

## Findings (Fable)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | The installer seeded kiosk + launch-on-startup on every install, including silent in-app updates of existing tills. That would override an owner's explicit choice, and it contradicted the help text ("a fresh install…"). | **Fixed.** `$INSTDIR\unitill-pos.exe` existing before `File /r` means an upgrade, which skips both the shortcut and provisioning. Verified on the VM: with normal + autostart off, an upgrade install left both unchanged and created no shortcut. |
| 2 | minor | `rcNormalPosition` is in workspace coordinates relative to the primary monitor; fitting it against the current monitor moved a secondary-screen window onto the primary. | **Fixed.** The placement is restored first, then `GetWindowRect` (screen coordinates) is fitted to the window's own monitor work area. |
| 3 | minor | Leaving fullscreen into maximized lost the restore rect, so Restore produced a monitor-sized window. | **Fixed.** Restore and fit first, then maximize. New Windows test `TestWin32MaximizedFromFullscreenRestoresToAFittedRect`. |
| 4 | minor | `getWindowLong` treated a legitimate 0 (the ex-style during fullscreen) as failure if a stray Win32 call set last-error. That could make exit-to-os refuse. | **Fixed.** `IsWindow` once, then style reads and writes need no last-error bookkeeping. |
| 5 | minor | `RPC_E_CHANGED_MODE` was not tolerated in `withCOM`. | **Fixed.** It's accepted as usable, with no `CoUninitialize`. |
| 6 | nit | A reinstall with autostart off recreated the shortcut. | **Fixed** by #1. |
| 7 | nit | fa help: "opens on the taskbar", and it used صندوق instead of the paragraph's دستگاه. | **Fixed.** |
| 8 | nit | `readShellLinkTarget` used a MAX_PATH buffer. | **Fixed.** It's now 32767. |
| 9 | info | The `windows-latest` job hasn't run yet. | Watched on the PR. |

The reviewer independently confirmed the COM vtable slots, struct sizes (WINDOWPLACEMENT 44, MONITORINFO 40), the negative-index sign extension, NSIS jump offsets and quoting, UI-thread affinity (`Dispatch` posts to a message-only window), and the de, ar and tr translations.

## Verified beyond automated tests

- **TDD re-checks, done by hand.**
  - The ack-race test failed with the real bug (`applied="kiosk"`) and passes with the fix, including under `-race`.
  - `TestWin32NormalRestoreFitsAnOversizedSavedFrame` fails on the unfixed build with the exact rect seen on the VM (`156,156,1200,944`) and passes with the fix.
  - A mutation (fullscreen 40 px short) makes `TestWin32FullscreenCoversMonitorAndNormalRestores` fail.
- **All 10 Windows tests pass on a real Windows 11 VM** (ARM64 build, run over SSH).
- **Device check on the VM** with a real Setup.exe built from this branch (amd64 under emulation). Each screenshot was looked at:
  - A fresh-path install wrote `Startup\Universal Till.lnk` → `unitill-desktop.exe`, and provisioning ran (`launch_on_startup` true).
  - Launch opened fullscreen covering the 1024×768 screen, with no title bar and no taskbar.
  - PIN-gated exit-to-os returned 204 in 0.14 s, giving back a framed window inside the work area with the taskbar visible. Before the ack fix it returned 503.
  - A live Settings switch to fullscreen: rect equals the monitor (1633×944 after a display resize). Maximized: a framed, standard maximized rect.
  - launch-on-startup off → the next launch removed the shortcut; on → the next launch recreated it.
  - Upgrade install: the owner's normal + off kept.
- **Not verified:** a real reboot or sign-in starting the till from the Startup shortcut (only the shortcut's presence and target were checked), touch input, multi-monitor hardware, real amd64 hardware.
- `go test ./...`: all green except `mobile`'s `TestStart_ListensOnAllInterfacesButReturnsLoopbackAddress`. It times out dialling this Mac's 172.27.x interface (environmental, and `mobile/` is untouched); it passed in this session's earlier full run.

## CI follow-ups on the PR

- `desktop-shell` deadcode baseline: the pure Win32 helpers had no caller in the Linux desktop build. They are now tagged `!desktop || windows`, so plain `go test` on any OS still runs them.
- `windows-shell` (first run on windows-latest): 10 of 11 tests passed. `TestReconcileStartupShortcutRoundTrip` compared the `.lnk` target with `os.Executable()`, which returned an 8.3 short path (`RUNNER~1`). The test now compares `GetLongPathName` forms. This was a test-only problem; the shortcut stores a valid long path. Re-run on the VM: PASS.
- `build`: guard-docs-shots needed the display topic re-shot (the help prose changed). `make docs-shots` changed only the four `display.png` files and the manifest.

## Verdict

Safe to merge once CI is green, including the new `windows-shell` job and the mingw step.

Deferred: #3735 (F11 toggle), #3736 (Windows kiosk lockdown via Assigned Access).
