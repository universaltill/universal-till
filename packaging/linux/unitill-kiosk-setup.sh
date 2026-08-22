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

echo "==> Configuring the Wayland screencast portal…"
# xdg-desktop-portal + its wlroots backend: on Wayland, Chromium delegates
# getDisplayMedia (screenshot / screen recording) to the portal's ScreenCast
# interface. With no portal installed the picker opens with NOTHING to
# select, so the bug-reporter's capture is unusable on a kiosk — confirmed on
# real field hardware, ut-docs#395. cage is wlroots-based, hence the -wlr
# backend. pipewire/wireplumber and dbus-user-session/dbus-bin are listed
# explicitly rather than relied on as Recommends (this script always runs
# --no-install-recommends): the portal advertises ScreenCast fine without
# them, but the actual capture stream fails at creation time — the same
# "happened to be present on the field devices, not guaranteed" gap this
# whole fix exists to close. Deliberately installed AFTER chromium, and
# tolerated rather than fatal: this is a screenshot dependency, not a
# boot-critical one, and must never be able to leave a till without its POS
# because a mirror hiccupped on an unrelated package.
if ! apt-get -o DPkg::Lock::Timeout=600 install -y --no-install-recommends xdg-desktop-portal xdg-desktop-portal-wlr pipewire wireplumber dbus-user-session dbus-bin; then
  echo "⚠ screencast portal packages failed to install — screen capture will not work on this device (ut-docs#395); continuing kiosk setup" >&2
fi

# Two pieces, both required (ut-docs#395):
#   1. xdg-desktop-portal picks its backend by XDG_CURRENT_DESKTOP; wlr.portal
#      declares UseIn=wlroots;sway;… so the session must say "wlroots" (the
#      launch script exports it and pushes it into the D-Bus activation
#      environment, since the portal is D-Bus-activated and does NOT inherit
#      Chromium's environment).
#   2. chooser_type=none makes xdg-desktop-portal-wlr auto-select the only
#      output instead of trying to show a chooser the kiosk cannot display —
#      which is also the behaviour the product owner asked for: pressing
#      "screenshot" on a till should just capture the screen.
# Both configs live under system-wide /etc paths, not the kiosk user's home:
# xdg-desktop-portal-wlr's own search order includes
# /etc/xdg/xdg-desktop-portal-wlr/config, so this needs no $KIOSK_USER lookup,
# no matching-group assumption, and no chown — the per-user path this
# replaced could `install -d -g "$KIOSK_USER"` and abort the entire setup
# under `set -e` if the account's primary group didn't share its name.
install -d -m 0755 /etc/xdg-desktop-portal
cat > /etc/xdg-desktop-portal/wlroots-portals.conf <<'EOF'
[preferred]
default=wlr
org.freedesktop.impl.portal.ScreenCast=wlr
org.freedesktop.impl.portal.Screenshot=wlr
EOF

install -d -m 0755 /etc/xdg/xdg-desktop-portal-wlr
cat > /etc/xdg/xdg-desktop-portal-wlr/config <<'EOF'
[screencast]
chooser_type=none
max_fps=30
EOF

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
# is the systemd alias every DM (lightdm/gdm3/sddm) registers itself under.
# `is-enabled` also succeeds for a target alias, so require a loaded service
# after resolving the alias before disabling anything.
has_real_display_manager() {
  display_manager_info=$(systemctl show --property=Id,LoadState --value display-manager.service 2>/dev/null || true)
  display_manager_id=$(printf '%s\n' "$display_manager_info" | sed -n '1p')
  display_manager_load_state=$(printf '%s\n' "$display_manager_info" | sed -n '2p')
  [ "$display_manager_load_state" = "loaded" ] || return 1
  case "$display_manager_id" in
    *.service) return 0 ;;
    *) return 1 ;;
  esac
}
if has_real_display_manager; then
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

# From here on, the kiosk itself is fully installed and will boot fine even
# if anything below fails — everything past this point is the ut-docs#883
# Settings-toggle enhancement (real enable/disable/start/stop control from
# the window-mode selector), which is layered on top and must never be able
# to leave a till without its kiosk over a problem in this optional part —
# same "tolerated rather than fatal" reasoning as the screencast-portal
# block above. Any failure here just means the Settings toggle keeps
# surfacing a clear error (ut-docs#883's own acceptance criteria) instead of
# actually flipping the service, until this is re-run successfully.

echo "==> Marking this box as a dedicated kiosk till (UT_KIOSK=1)…"
# A systemd drop-in, not /opt/unitill/pos.env: pos.env is a dpkg conffile
# (packaging/pos.env.example -> /opt/unitill/pos.env, config|noreplace,
# independent review F8) — scripting edits into it risks a future release's
# conffile prompt, or --force-confnew silently reverting this. A drop-in is
# root-owned (no pos:pos chown to worry about), idempotent to overwrite, and
# needs no directory that might not exist yet. pages.Init reads UT_KIOSK (or
# falls back to detecting unitill-kiosk.service itself, ut-docs#883 review
# F1) to select the real KioskSystemdWindowController.
install -d -m 0755 /etc/systemd/system/unitill-pos.service.d
cat > /etc/systemd/system/unitill-pos.service.d/kiosk.conf << 'EOF'
[Service]
Environment=UT_KIOSK=1
EOF
systemctl daemon-reload
# A unitill-pos already running (e.g. re-running this script after first
# boot) started before this drop-in existed and won't see it without a
# restart — best-effort: on a genuinely fresh install the service may not be
# up yet, which is fine, it reads its environment at its own first start.
systemctl restart unitill-pos.service 2>/dev/null || true

echo "==> Granting the till service a scoped kiosk-service toggle (ut-docs#883)…"
# unitill-pos runs as the unprivileged `pos` system user (unitill-
# pos.service, User=pos) with no standing permission to enable/disable/
# start/stop ANY systemd unit. The Settings window-mode toggle needs exactly
# four calls against unitill-kiosk.service — grant precisely those four, no
# wildcard, no other unit (smallest privilege that works, per the
# ecosystem's security-first rule). Written to a DOT-PREFIXED temp file
# inside /etc/sudoers.d first (independent review F2): sudo's own directory
# scan skips dotfiles, so it is inert even before validation, then validated
# with `visudo -c` and only `install`ed under its real name once that
# passes — the drop-in is never live in a possibly-broken state, which
# matters on a first-boot box where a power cut mid-write is a realistic
# event and a malformed drop-in can lock out ALL sudo, not just this grant.
# Skipped (not fatal) if `sudo` isn't installed at all — plausible on a
# plain Debian box per this script's own header, and this whole feature is
# optional on top of an already-working kiosk.
if command -v visudo >/dev/null 2>&1; then
  SYSTEMCTL_BIN="$(command -v systemctl)"
  TMP_SUDOERS="$(mktemp /etc/sudoers.d/.unitill-kiosk.XXXXXX)"
  cat > "$TMP_SUDOERS" << EOF
pos ALL=(root) NOPASSWD: ${SYSTEMCTL_BIN} enable unitill-kiosk.service, ${SYSTEMCTL_BIN} disable unitill-kiosk.service, ${SYSTEMCTL_BIN} start unitill-kiosk.service, ${SYSTEMCTL_BIN} stop unitill-kiosk.service
EOF
  chmod 0440 "$TMP_SUDOERS"
  if visudo -c -f "$TMP_SUDOERS" >/dev/null 2>&1; then
    install -m 0440 -o root -g root "$TMP_SUDOERS" /etc/sudoers.d/unitill-kiosk
    rm -f "$TMP_SUDOERS"
  else
    echo "⚠ generated sudoers drop-in failed visudo -c — not installing (Settings window-mode toggle will show a clear error until this is fixed)" >&2
    rm -f "$TMP_SUDOERS"
  fi
else
  echo "⚠ visudo not found — skipping the scoped kiosk-service sudoers grant (Settings window-mode toggle will show a clear error until this is fixed)" >&2
fi

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
