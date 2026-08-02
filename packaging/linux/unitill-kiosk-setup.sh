#!/usr/bin/env bash
# Universal Till kiosk setup — Raspberry Pi OS Lite / Debian (no desktop).
#
# Turns a headless box with a screen into a dedicated till appliance:
# power on → till service starts → the screen goes straight to the POS,
# fullscreen. No desktop, no browser chrome, nothing else visible.
#
# On a fresh .deb install on a Pi OS Lite box this runs AUTOMATICALLY on the
# first boot (the postinstall stages unitill-kiosk-firstboot.service, which
# invokes this with --auto). The manual flow below still applies to desktop
# images, upgrades, and non-Pi Debian boxes:
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
#       (plus `sudo touch /etc/unitill/no-kiosk` on a Pi, so a later
#       reinstall's first-boot setup doesn't re-enable it)
set -euo pipefail

[ "$(id -u)" = 0 ] || { echo "Run with sudo."; exit 1; }

# --auto: non-interactive first-boot mode (invoked by
# unitill-kiosk-firstboot.service, staged by the .deb postinstall on a Pi).
# No SUDO_USER exists there, so fall back to the image's primary user (uid
# 1000 — "pi" on Pi OS, whatever was chosen in the imager elsewhere).
AUTO=0
[ "${1:-}" = "--auto" ] && AUTO=1
KIOSK_USER="${SUDO_USER:-}"
if [ -z "$KIOSK_USER" ]; then
  KIOSK_USER="$(getent passwd 1000 | cut -d: -f1 || true)"
fi
KIOSK_USER="${KIOSK_USER:-pi}"
if ! id "$KIOSK_USER" >/dev/null 2>&1; then
  if [ "$AUTO" = 1 ]; then
    # First boot on an image with no uid-1000 user and no "pi": nobody is
    # around to answer, so create a dedicated login user for the compositor
    # (cage needs a real PAM login user, not a --system/nologin account).
    KIOSK_USER="utkiosk"
    id "$KIOSK_USER" >/dev/null 2>&1 || useradd -m -s /bin/bash "$KIOSK_USER"
  else
    echo "User $KIOSK_USER not found — run via sudo from the login user."
    exit 1
  fi
fi

echo "==> Installing kiosk packages (cage + chromium)…"
# The dpkg lock timeout matters on a Pi first boot: apt-daily/unattended-
# upgrades commonly hold the lock right after network-online, and failing
# instantly would leave the till at a console until the next boot.
apt-get -o DPkg::Lock::Timeout=600 update
# kbd provides chvt (the service's ExecStartPre needs it); seatd is
# deliberately NOT installed: the service runs under PAMName=login (a logind
# session), and with seatd present libseat picks the seatd backend and dies
# with "Could not poll connection: Broken pipe" (proven on Pi5/trixie,
# ut-docs#6).
apt-get -o DPkg::Lock::Timeout=600 install -y --no-install-recommends cage curl kbd
# Chromium's package name differs between Raspberry Pi OS and plain Debian.
apt-get -o DPkg::Lock::Timeout=600 install -y --no-install-recommends chromium-browser 2>/dev/null ||
  apt-get -o DPkg::Lock::Timeout=600 install -y --no-install-recommends chromium

echo "==> Installing emoji font (menu icons, plugin/status glyphs)…"
# The till UI's icons are plain Unicode emoji text, and a base Raspberry Pi
# OS image ships no color-emoji font at all -- without this every menu icon
# renders as a blank box (confirmed on a real field device, 2026-07-29).
# Installed explicitly here because this flow's documented install command
# (`apt install --no-install-recommends ./unitill-pos_*.deb`, see header)
# deliberately bypasses the .deb's `recommends` list, which also carries it.
apt-get -o DPkg::Lock::Timeout=600 install -y --no-install-recommends fonts-noto-color-emoji

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
# Root-owned, outside /opt/unitill (postinstall.sh chowns that whole tree to
# pos for self-update, ut-docs#151) — this is root-executed via cage below,
# so it must not sit somewhere pos can rewrite (ut-docs#255).
install -D -m 0755 "$(dirname "$0")/unitill-kiosk-launch.sh" /usr/lib/unitill/unitill-kiosk-launch

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
# Debian 13 (trixie) on the Pi5 boots with the active VT on tty7; cage is
# pinned to tty1, so its logind session never becomes active and it dies with
# "libseat … Timeout waiting session to become active". Force the VT first
# ("+" = run as root despite User=). Proven on the field Pi5 (ut-docs#6).
ExecStartPre=+/usr/bin/chvt 1
# PAMName=login gives cage a logind session — make libseat use it even if a
# stray seatd is installed (else: "Could not poll connection: Broken pipe").
Environment=LIBSEAT_BACKEND=logind
ExecStart=/usr/bin/cage -d -- /usr/lib/unitill/unitill-kiosk-launch
Restart=always
RestartSec=3

[Install]
WantedBy=graphical.target
EOF

systemctl daemon-reload
systemctl enable unitill-kiosk.service
systemctl set-default graphical.target

# The marker tells the staged first-boot unit this box is done — written by
# BOTH paths (a manual setup must also stop a later first-boot run from
# re-doing it, e.g. re-enabling a kiosk the owner has since undone).
mkdir -p /var/lib/unitill
if [ "$AUTO" = 1 ]; then
  # First-boot flow: bring the kiosk up right now (chvt in ExecStartPre
  # switches the console) instead of asking a shop owner to reboot.
  # --no-block: a blocking start from inside a boot-time unit can stall on
  # the Conflicts=getty@tty1 stop job. Success = the service is actually
  # active — only then write the marker; a dead kiosk must leave the
  # first-boot unit armed to retry (independent review, 2026-07-31).
  systemctl start --no-block unitill-kiosk.service
  for _ in $(seq 1 30); do
    systemctl is-active --quiet unitill-kiosk.service && break
    sleep 1
  done
  if ! systemctl is-active --quiet unitill-kiosk.service; then
    echo "✗ Kiosk service did not become active — will retry (journalctl -u unitill-kiosk)" >&2
    exit 1
  fi
  touch /var/lib/unitill/kiosk-setup-done
  echo "✓ Kiosk installed and started (first-boot auto setup)."
else
  touch /var/lib/unitill/kiosk-setup-done
  echo
  echo "✓ Kiosk installed. Reboot to start:  sudo reboot"
  echo "  The screen will show the till fullscreen. To get a console instead,"
  echo "  switch VT (Ctrl+Alt+F2) or ssh in."
fi
