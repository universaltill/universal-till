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
has_real_display_manager() {
    # `is-enabled` reports success for an alias as well as a real display
    # manager. On the field Pi, the alias resolved to graphical.target, so
    # it must not make a headless appliance look like a desktop install.
    display_manager_info=$(systemctl show --property=Id,LoadState --value display-manager.service 2>/dev/null || true)
    display_manager_id=$(printf '%s\n' "$display_manager_info" | sed -n '1p')
    display_manager_load_state=$(printf '%s\n' "$display_manager_info" | sed -n '2p')
    [ "$display_manager_load_state" = "loaded" ] || return 1
    case "$display_manager_id" in
        *.service) return 0 ;;
        *) return 1 ;;
    esac
}

# Shared fresh-install gates for BOTH auto-setup branches below (headless
# appliance and desktop kiosk overlay, ut-docs#1040). One author for the
# environment checks, so the two branches can never drift into overlapping:
# they differ ONLY on the resolved display manager (and the overlay's own
# opt-out marker), which makes them mutually exclusive by construction.
is_fresh_install_pi_debian() {
    [ "$1" = "configure" ] || return 1
    [ -z "$2" ] || return 1                 # upgrade → never auto-stage
    [ -d /run/systemd/system ] || return 1
    grep -qs "Raspberry Pi" /proc/device-tree/model || return 1
    grep -qsE '^ID=(debian|raspbian)' /etc/os-release || return 1
    return 0
}

is_pi_appliance() {
    is_fresh_install_pi_debian "$1" "$2" || return 1
    if has_real_display_manager; then
        return 1                            # desktop image → overlay branch below
    fi
    return 0
}

# ut-docs#1040: the OTHER Pi shape — a fresh install on a Pi that DOES run a
# desktop OS (Raspberry Pi OS "with Desktop", what a shop owner actually
# flashes). The owner asked for exactly this: the till fullscreen (kiosk) ON
# TOP of the existing desktop session, with a PIN-gated way back to the
# desktop — never the headless cage takeover, which would cost them the
# desktop they chose that image for. Opt out any time, BEFORE installing:
#   sudo touch /etc/unitill/no-desktop-kiosk-overlay
# (deliberately distinct from /etc/unitill/no-kiosk, which already means
# "never kiosk-ify this box's console" for the headless path).
is_desktop_kiosk_overlay() {
    is_fresh_install_pi_debian "$1" "$2" || return 1
    has_real_display_manager || return 1    # headless → appliance branch above
    [ ! -e /etc/unitill/no-desktop-kiosk-overlay ] || return 1
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

# Desktop kiosk overlay (ut-docs#1040): everything app-side already exists
# and keys off two persisted settings plus an XDG autostart entry —
#   - `unitill-pos provision-desktop-kiosk-defaults` seeds
#     display.window_mode=kiosk + display.launch_on_startup=true through the
#     repository layer (never raw SQL from this script) and records a
#     system-actor audit entry, so the decision is visible to the owner on
#     the till's own /audit page, not just in this install log. Idempotent
#     (a DB-side completion marker), so a re-run never clobbers a window
#     mode the owner has since changed. Run as the pos service user against
#     the service's own data dir — the same UT_DATA_DIR unitill-pos.service
#     pins — so it seeds the database the running till actually reads.
#   - `unitill-desktop --install-autostart`, run once as the detected login
#     user, writes ~/.config/autostart/unitill.desktop via the SAME Go code
#     (reconcileAutostart) that owns that entry on every normal launch — the
#     entry format has exactly one author, never a bash-heredoc copy here.
# From then on the existing, already-tested flow does the rest: the desktop
# session autostarts unitill-desktop, which fetches the seeded prefs from
# the service and goes fullscreen kiosk; the PIN-gated "Exit to OS window"
# in Settings hands the screen back to the desktop (ut-docs#883/#1039).
if is_desktop_kiosk_overlay "${1:-}" "${2:-}"; then
    mkdir -p /etc/unitill
    # Login user: same rule as unitill-kiosk-setup.sh --auto (no SUDO_USER
    # exists inside a dpkg transaction) — uid 1000, falling back to "pi".
    # Unlike --auto we never CREATE a user here: with no login user there is
    # no desktop session to overlay, so we log and leave the box alone.
    OVERLAY_USER="$(getent passwd 1000 | cut -d: -f1 || true)"
    OVERLAY_USER="${OVERLAY_USER:-pi}"
    if ! id "$OVERLAY_USER" >/dev/null 2>&1; then
        echo "Desktop Pi detected, but no login user (uid 1000 or 'pi') found — skipping the fullscreen-till setup."
        echo "Run this once as your desktop user to finish it: /opt/unitill/bin/unitill-desktop --install-autostart"
    else
        AUTOSTART_STAGED=false
        # Can fail legitimately (e.g. installed with --no-install-recommends,
        # so the GTK/WebKit libs unitill-desktop links against are absent and
        # the dynamic linker refuses to exec it) — never fail the install
        # over it; the audit entry records the honest outcome either way.
        if runuser -u "$OVERLAY_USER" -- /opt/unitill/bin/unitill-desktop --install-autostart; then
            AUTOSTART_STAGED=true
        else
            echo "Warning: could not stage the till's autostart entry for user '$OVERLAY_USER'." >&2
            echo "Run this once as that user to finish it: /opt/unitill/bin/unitill-desktop --install-autostart" >&2
        fi
        if runuser -u pos -- env UT_ENV_FILE=/opt/unitill/pos.env UT_DATA_DIR=/opt/unitill/data /opt/unitill/bin/unitill-pos provision-desktop-kiosk-defaults --trigger=deb-postinstall --autostart-staged="$AUTOSTART_STAGED"; then
            # MUST restart: unitill-pos.service was already (re)started
            # further up, BEFORE these two settings existed, and the running
            # server caches them in memory (common.RuntimeState, seeded once
            # by pages.Init's LoadState). Without this restart the seeding
            # above is silently undone twice over:
            #   - GET /api/window-mode answers from that stale cache, so the
            #     autostarted desktop shell reads launch_on_startup=false and
            #     its own reconcileAutostart(false) DELETES the autostart
            #     entry we just staged — the overlay uninstalls itself on the
            #     first login;
            #   - the first-boot wizard (and every Settings save) calls
            #     common.SaveState, which rewrites the WHOLE settings map
            #     from that stale cache, putting window_mode back to
            #     "normal" and launch_on_startup back to "false".
            # try-restart, not restart: a no-op if the service isn't running
            # (it will then read the seeded values on its own first start).
            # Never fail the install on it, same as the restart above.
            systemctl try-restart unitill-pos.service >/dev/null 2>&1 || true
        else
            echo "Warning: could not seed the fullscreen-till defaults — pick 'Kiosk' under Settings → Display instead." >&2
        fi
        echo "Raspberry Pi with a desktop detected: the till will open fullscreen over this desktop at login (user: $OVERLAY_USER)."
        echo "Leave it any time via Settings → Display → 'Exit to OS window' (admin PIN)."
        echo "To keep this desktop overlay-free instead: sudo touch /etc/unitill/no-desktop-kiosk-overlay"
    fi
fi

echo "Universal Till installed. Open http://localhost:8080 — the first-boot"
echo "wizard sets language, currency and the admin PIN."
echo "Config: /opt/unitill/pos.env · Data: /opt/unitill/data"
