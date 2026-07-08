# Universal Till on a Raspberry Pi (dedicated till)

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
