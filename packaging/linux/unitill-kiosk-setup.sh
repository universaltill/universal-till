#!/usr/bin/env bash
# Universal Till kiosk setup — Raspberry Pi OS Lite / Debian (no desktop).
#
# Turns a headless box with a screen into a dedicated till appliance:
# power on → till service starts → the screen goes straight to the POS,
# fullscreen. No desktop, no browser chrome, nothing else visible.
#
#   1. Install the till:   sudo apt install --no-install-recommends ./unitill-pos_*_arm64.deb
#                          (skips the desktop-app GTK/WebKit libs -- this
#                          script installs cage+Chromium instead, so a
#                          headless Lite box doesn't need both stacks)
#   2. Run this once:      sudo bash unitill-kiosk-setup.sh
#   3. Reboot:             sudo reboot
#
# Stack: cage (single-app Wayland kiosk compositor) + Chromium --kiosk.
# Undo: sudo systemctl disable --now unitill-kiosk && sudo systemctl set-default multi-user.target
set -euo pipefail

[ "$(id -u)" = 0 ] || { echo "Run with sudo."; exit 1; }
# Prefer the sudo caller (manual `sudo bash unitill-kiosk-setup.sh` run); when
# invoked without sudo (e.g. a future root-run automation with no SUDO_USER),
# fall back to the first regular non-system account -- current Raspberry Pi
# Imager images ask for a custom username since Bookworm, there is no "pi"
# user by default anymore. Last resort: "pi", for older images. Reads
# /etc/passwd directly rather than piping `getent passwd` through `awk
# '{exit}'` -- the early exit can SIGPIPE getent on a large passwd source
# (e.g. NSS/LDAP) under this script's `pipefail`, silently aborting setup.
KIOSK_USER="${SUDO_USER:-$(awk -F: '$3 >= 1000 && $3 < 60000 {print $1; exit}' /etc/passwd)}"
KIOSK_USER="${KIOSK_USER:-pi}"
id "$KIOSK_USER" >/dev/null 2>&1 || { echo "User $KIOSK_USER not found — run via sudo from the login user."; exit 1; }

echo "==> Installing kiosk packages (cage + chromium)…"
apt-get update
# kbd: provides /usr/bin/chvt, which the generated unit's ExecStartPre
# needs below -- without it the unit fails to start at all on any image
# that doesn't already carry kbd (confirmed absent on a plain Debian box).
apt-get install -y --no-install-recommends cage seatd kbd curl
# Chromium's package name differs between Raspberry Pi OS and plain Debian.
apt-get install -y --no-install-recommends chromium-browser 2>/dev/null ||
  apt-get install -y --no-install-recommends chromium

echo "==> Installing emoji font (menu icons, plugin/status glyphs)…"
# The till UI's icons are plain Unicode emoji text, and a base Raspberry Pi
# OS image ships no color-emoji font at all -- without this every menu icon
# renders as a blank box (confirmed on a real field device, 2026-07-29).
# Installed explicitly here because this flow's documented install command
# (`apt install --no-install-recommends ./unitill-pos_*.deb`, see header)
# deliberately bypasses the .deb's `recommends` list, which also carries it.
apt-get install -y --no-install-recommends fonts-noto-color-emoji

echo "==> Kiosk user permissions (video/input)…"
for grp in video render input seat; do
  getent group "$grp" >/dev/null && usermod -aG "$grp" "$KIOSK_USER" || true
done

# Raspberry Pi OS "with Desktop" images (unlike Lite, which this script's
# header assumes) ship a display manager (lightdm by default) enabled on
# graphical.target -- the same target this script points at. Left running,
# it grabs the console/seat first and cage silently never gets a display
# (confirmed live, 2026-07-29: cage process ran with zero children, no
# error logged anywhere -- lightdm had already won the seat). `display-manager`
# is the systemd alias every DM (lightdm/gdm3/sddm) registers itself under,
# so this doesn't need to special-case which one is installed.
if systemctl is-enabled display-manager.service >/dev/null 2>&1 || systemctl is-active display-manager.service >/dev/null 2>&1; then
  echo "==> Disabling the desktop display manager (conflicts with the kiosk console)…"
  systemctl disable --now display-manager.service
fi

echo "==> Installing the kiosk launcher…"
install -D -m 0755 "$(dirname "$0")/unitill-kiosk-launch.sh" /opt/unitill/bin/unitill-kiosk-launch

echo "==> Kiosk service (cage on tty1, restarts if it ever dies)…"
cat > /etc/systemd/system/unitill-kiosk.service << EOF
[Unit]
Description=Universal Till kiosk (fullscreen POS on the console)
After=systemd-user-sessions.service unitill-pos.service
Wants=unitill-pos.service
Conflicts=getty@tty1.service

[Service]
User=${KIOSK_USER}
PAMName=login
TTYPath=/dev/tty1
# Debian 13 (trixie) on Pi5 boots with the active VT on tty7, not tty1 --
# without forcing it, cage's logind session never becomes active and cage
# dies with "Timeout waiting session to become active" (confirmed live on a
# field Pi5, 2026-07-30). The leading "+" runs this as root regardless of
# the service's own User=.
ExecStartPre=+/usr/bin/chvt 1
# This service runs under PAMName=login (a logind session), but seatd is
# also installed above -- without pinning the backend, libseat picks seatd
# instead and fails "Could not poll connection: Broken pipe" (confirmed live
# on the same field Pi5).
Environment=LIBSEAT_BACKEND=logind
StandardInput=tty
StandardOutput=journal
StandardError=journal
UtmpIdentifier=tty1
ExecStart=/usr/bin/cage -d -- /opt/unitill/bin/unitill-kiosk-launch
Restart=always
RestartSec=3

[Install]
WantedBy=graphical.target
EOF

systemctl daemon-reload
systemctl enable unitill-kiosk.service
systemctl set-default graphical.target

echo
echo "✓ Kiosk installed. Reboot to start:  sudo reboot"
echo "  The screen will show the till fullscreen. To get a console instead,"
echo "  switch VT (Ctrl+Alt+F2) or ssh in."
