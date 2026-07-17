# Code review — Linux desktop app + Raspberry Pi kiosk mode

**Date:** 2026-07-17
**Branch:** `feat/linux-desktop-kiosk`
**Ask (Farshid):** "if it is [Pi OS] Lite, package with browser kiosk mode;
if it is desktop with UI, we can have an app like [mac/windows]."

## Two delivery modes, matching how Linux tills are actually deployed

1. **Desktop app** (Debian/Ubuntu/Pi OS with a desktop): `unitill-desktop`
   built for linux amd64+arm64 (webview_go → GTK3/WebKitGTK). The `.deb` puts
   "Universal Till" in the applications menu (`.desktop` entry + 512px icon);
   the `tar.gz` carries the binary. Same shell as mac/windows, two Linux
   twists in `cmd/unitill-desktop/desktop.go`:
   - **Attach mode**: the `.deb` already runs the till as a systemd service
     on :8080 — the shell probes `/healthz` first and, when a till answers,
     opens the window on it instead of spawning a second server (which would
     fight over the SQLite database). Also makes a second launch join the
     first. Safe on mac/windows too (the mac shell ignores non-positive
     child pids by existing guard).
   - **workDir**: also tries the parent dir (`/opt/unitill/bin` →
     `/opt/unitill/web`), not just the mac `../Resources` layout.
2. **Kiosk mode** (Pi OS **Lite** — no desktop): `sudo
   /opt/unitill/bin/unitill-kiosk-setup`, reboot → the box boots straight
   into the till fullscreen. Stack: **cage** (single-app Wayland kiosk
   compositor) + Chromium `--kiosk`, a systemd unit on tty1 (autorestart,
   `Conflicts=getty@tty1`), launcher waits for `/healthz`. Undo command
   documented in the script header.

## Build/packaging

- `release.yml`: new `linux-shells` matrix job (ubuntu-22.04 /
  **ubuntu-22.04-arm**) builds the CGO shell natively per arch — webview_go
  pins `webkit2gtk-4.0`, the Debian bookworm / Pi OS / Ubuntu 22.04 ABI
  (Ubuntu 24.04 desktop hosts would need the 4.1 ABI — known limitation,
  revisit when webview_go moves). goreleaser downloads the artifacts and
  archives them via `files:` (`dist-shells/linux-{{ .Arch }}/`).
- `.deb`: shell + `.desktop` + icon + kiosk scripts; GTK/WebKit are
  **Suggests**, not Depends — headless/kiosk installs stay lean, and any
  Linux desktop already has them.

## Verification

- Local snapshot release with stub shell artifacts: linux tar.gz and .deb
  verified to contain the shell, kiosk scripts, `.desktop`, icon; control
  file carries the Suggests. mac + windows shell builds still compile.
- ⚠️ **Not run on real Linux hardware** — no Pi/Linux box here. The CI
  linux-shells job is exercised by the release itself; the kiosk script
  follows the standard cage+chromium recipe but needs one real Pi OS Lite
  boot test (Farshid has the Pi). Failure mode is contained: kiosk is
  opt-in, and the desktop shell is an extra file that changes nothing for
  existing service/browser users.
