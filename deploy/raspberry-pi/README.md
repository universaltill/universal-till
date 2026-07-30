# Universal Till on a Raspberry Pi (dedicated till)

> **⚠️ Deprecated, unsupported on current Raspberry Pi OS.** This X11/
> autologin path needs "Pi OS with Desktop" and a desktop autologin session
> — current Raspberry Pi OS tracks Debian 13 (trixie), which no longer sets
> this up as cleanly, and this path has never received the tty1/logind
> fixes proven necessary on a real Pi5 (see `packaging/linux/
> unitill-kiosk-setup.sh`'s `ExecStartPre=chvt 1` / `LIBSEAT_BACKEND`).
> **Use `packaging/linux/unitill-kiosk-setup.sh` instead** (Wayland/cage) —
> that's the path the `.deb` ships and now auto-enables on Pi hardware with
> no manual step at all. Kept here for reference only, not deleted.

Turns a Raspberry Pi into a boot-to-POS kiosk:

- **`unitill-pos.service`** — runs the POS binary on boot (`UT_KIOSK=1` enables
  the touch-first UI: bigger targets, no text selection), restarts on failure,
  keeps working offline.
- **`unitill-kiosk.service`** — waits for the POS to answer, then launches
  Chromium fullscreen (`--kiosk`) pointed at `http://127.0.0.1:8080/`.

## Install

```bash
git clone https://github.com/universaltill/universal-till
cd universal-till
sudo deploy/raspberry-pi/install.sh
sudo reboot
```

The Pi boots straight into the fullscreen POS.

## Notes

- Requires Raspberry Pi OS **with desktop** and autologin for the kiosk
  session (raspi-config → System → Boot / Auto Login → Desktop Autologin).
- Configuration lives in `/opt/unitill/pos.env` (marketplace endpoint, signing
  public key, merchant/store/device ids).
- Exit the kiosk for maintenance: `Ctrl+Alt+F2` for a console, then
  `sudo systemctl stop unitill-kiosk`.
- Update: rerun `install.sh` from a fresh checkout (data in `/opt/unitill/data`
  is preserved).
