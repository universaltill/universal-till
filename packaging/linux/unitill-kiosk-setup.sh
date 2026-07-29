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
KIOSK_USER="${SUDO_USER:-pi}"
id "$KIOSK_USER" >/dev/null 2>&1 || { echo "User $KIOSK_USER not found — run via sudo from the login user."; exit 1; }

echo "==> Installing kiosk packages (cage + chromium)…"
apt-get update
apt-get install -y --no-install-recommends cage seatd curl
# Chromium's package name differs between Raspberry Pi OS and plain Debian.
apt-get install -y --no-install-recommends chromium-browser 2>/dev/null ||
  apt-get install -y --no-install-recommends chromium

echo "==> Kiosk user permissions (video/input)…"
for grp in video render input seat; do
  getent group "$grp" >/dev/null && usermod -aG "$grp" "$KIOSK_USER" || true
done

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
