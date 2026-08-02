#!/bin/sh
# Universal Till .deb postinstall: system user, writable data dirs, item
# images preserved across upgrades, service enabled.
set -e

# System user for the service.
if ! id -u pos >/dev/null 2>&1; then
    useradd --system --home /opt/unitill --shell /usr/sbin/nologin pos
fi

mkdir -p /opt/unitill/data /var/lib/unitill/items

# web/ is package-owned and refreshed on every upgrade — but shops upload
# item photos INTO web/public/assets/items. Keep those on /var/lib behind a
# symlink so upgrades never delete them; seed the shipped demo thumbs only
# into an empty store.
ITEMS=/opt/unitill/web/public/assets/items
if [ -d "$ITEMS" ] && [ ! -L "$ITEMS" ]; then
    if [ -z "$(ls -A /var/lib/unitill/items 2>/dev/null)" ]; then
        cp -R "$ITEMS/." /var/lib/unitill/items/ 2>/dev/null || true
    fi
    rm -rf "$ITEMS"
fi
ln -sfn /var/lib/unitill/items "$ITEMS"

chown -R pos:pos /var/lib/unitill

# Own the WHOLE install tree as the pos service user, not just data/assets.
# selfupdate.Supported() requires both /opt/unitill/bin (the binary's dir)
# and /opt/unitill (unitill-pos.service's WorkingDirectory, where Apply()
# swaps web/) writable by pos, or a fresh .deb install has no in-app update
# path at all (ut-docs#151) — the retired deploy/raspberry-pi/install.sh
# covered this with the same chown; do it here too. Runs on every
# invocation (fresh install AND upgrade): dpkg re-extracts package files as
# root-owned on every upgrade, so this must reassert every time, not just
# once. The root-executed kiosk helpers (unitill-kiosk-setup,
# unitill-kiosk-launch) deliberately live outside this tree, at
# /usr/lib/unitill — never pos-writable (ut-docs#255), or the pos service
# user could plant a script root re-executes via unitill-kiosk-firstboot.service.
chown -R pos:pos /opt/unitill

# ut-docs#255 migration: unitill-kiosk-firstboot.service and
# unitill-kiosk.service are NOT dpkg-managed (both written by heredoc, one
# by this script and one by unitill-kiosk-setup.sh, not shipped as package
# `contents:`), so a box that installed or ran kiosk setup before this fix
# still has one or both referencing the old pos-writable /opt/unitill/bin
# path — dpkg moving the *package's* copy of the scripts to /usr/lib/unitill
# does nothing to those already-written unit files. Without this, the
# pos->root path this ticket exists to close survives upgrading on every
# box that installed before it. Runs on every invocation (fresh install AND
# upgrade), before the fresh-install-only gate below — same reasoning as
# the chown above. Idempotent: every check is a no-op once migrated.
if [ -f /opt/unitill/bin/unitill-kiosk-launch ] && [ ! -e /usr/lib/unitill/unitill-kiosk-launch ]; then
    # Carry the already-installed launch script to its new home first —
    # it's self-copied by unitill-kiosk-setup.sh's own `install -D`, not
    # dpkg-owned, so the new package version doesn't put a copy at the new
    # path on its own. Without this, repointing unitill-kiosk.service below
    # would reference a path nothing has put a file at yet.
    mkdir -p /usr/lib/unitill
    cp -p /opt/unitill/bin/unitill-kiosk-launch /usr/lib/unitill/unitill-kiosk-launch
fi
if [ -f /etc/systemd/system/unitill-kiosk-firstboot.service ] && grep -q '/opt/unitill/bin/unitill-kiosk-setup' /etc/systemd/system/unitill-kiosk-firstboot.service; then
    sed -i 's#/opt/unitill/bin/unitill-kiosk-setup#/usr/lib/unitill/unitill-kiosk-setup#g' /etc/systemd/system/unitill-kiosk-firstboot.service
fi
if [ -f /etc/systemd/system/unitill-kiosk.service ] && grep -q '/opt/unitill/bin/unitill-kiosk-launch' /etc/systemd/system/unitill-kiosk.service; then
    sed -i 's#/opt/unitill/bin/unitill-kiosk-launch#/usr/lib/unitill/unitill-kiosk-launch#g' /etc/systemd/system/unitill-kiosk.service
fi

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload
    systemctl enable unitill-pos.service >/dev/null 2>&1 || true
    # Start (or restart after upgrade) — never fail the install on it.
    systemctl restart unitill-pos.service >/dev/null 2>&1 || true
fi

# On a Raspberry Pi appliance, a FRESH install should boot straight into the
# fullscreen POS — a shop owner will never run the manual kiosk setup
# (ut-docs#6). The setup itself calls apt, which can't run inside this dpkg
# transaction, so stage a one-shot first-boot service that runs it on the
# next boot. Deliberately narrow (independent review, 2026-07-31):
#   - fresh installs only ($1=configure with empty $2) — an UPGRADE must
#     never convert an existing field Pi that chose not to run the kiosk;
#   - Debian-family Pi OS only (Ubuntu-on-Pi resolves chromium to a snap
#     that doesn't reliably run under cage);
#   - no enabled display manager — a Pi OS "with Desktop" box keeps its
#     desktop; kiosk-ifying it (which disables the DM) stays a deliberate
#     manual `unitill-kiosk-setup` call;
#   - opt out any time: sudo touch /etc/unitill/no-kiosk
is_pi_appliance() {
    [ "$1" = "configure" ] || return 1
    [ -z "$2" ] || return 1                 # upgrade → never auto-stage
    [ -d /run/systemd/system ] || return 1
    grep -qs "Raspberry Pi" /proc/device-tree/model || return 1
    grep -qsE '^ID=(debian|raspbian)' /etc/os-release || return 1
    if systemctl is-enabled display-manager.service >/dev/null 2>&1; then
        return 1                            # desktop image → manual opt-in only
    fi
    return 0
}
if is_pi_appliance "${1:-}" "${2:-}"; then
    mkdir -p /etc/unitill
    cat > /etc/systemd/system/unitill-kiosk-firstboot.service << 'EOF'
[Unit]
Description=Universal Till kiosk first-boot setup (installs cage/chromium, enables the kiosk)
Wants=network-online.target
After=network-online.target unitill-pos.service
ConditionPathExists=!/etc/unitill/no-kiosk
ConditionPathExists=!/var/lib/unitill/kiosk-setup-done
ConditionPathExists=/usr/lib/unitill/unitill-kiosk-setup

[Service]
Type=oneshot
# The setup writes /var/lib/unitill/kiosk-setup-done itself, only after
# verifying the kiosk service actually came up — a failed run leaves no
# marker, stays enabled, and retries (offline first boot, apt lock, ...).
ExecStart=/usr/lib/unitill/unitill-kiosk-setup --auto
ExecStartPost=/usr/bin/systemctl disable unitill-kiosk-firstboot.service
Restart=on-failure
RestartSec=60
TimeoutStartSec=30min

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable unitill-kiosk-firstboot.service >/dev/null 2>&1 || true
    echo "Raspberry Pi appliance detected: fullscreen kiosk will be set up on next boot."
    echo "To keep this Pi kiosk-free instead: sudo touch /etc/unitill/no-kiosk"
fi

echo "Universal Till installed. Open http://localhost:8080 — the first-boot"
echo "wizard sets language, currency and the admin PIN."
echo "Config: /opt/unitill/pos.env · Data: /opt/unitill/data"
