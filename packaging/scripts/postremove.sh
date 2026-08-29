#!/bin/sh
# On PURGE (apt remove --purge), delete the shop data too. A plain removal
# (apt remove) deliberately keeps /var/lib/unitill and /opt/unitill/data so a
# reinstall picks up the existing database. dpkg passes the action as $1.
set -e

# Kiosk units were written at install/setup time (not package-owned): remove
# them on any removal, and give the console back to getty if this box had
# been kiosk-ified (graphical.target with no DM and no kiosk = black screen).
if [ "$1" = "remove" ] || [ "$1" = "purge" ]; then
    if [ -f /etc/systemd/system/unitill-kiosk.service ]; then
        systemctl set-default multi-user.target >/dev/null 2>&1 || true
    fi
    rm -f /etc/systemd/system/unitill-kiosk.service \
          /etc/systemd/system/unitill-kiosk-firstboot.service
    rm -rf /etc/systemd/system/unitill-kiosk.service.d
    # The uninstaller's PATH symlink is postinstall-created (not
    # package-owned, like the kiosk units above) — remove it here or a
    # purge leaves /usr/bin/unitill-uninstall dangling at a deleted
    # binary (ut-docs#1083). Guarded by remove/purge so an upgrade's
    # postrm never strips it.
    rm -f /usr/bin/unitill-uninstall
    if [ -d /run/systemd/system ]; then
        systemctl daemon-reload >/dev/null 2>&1 || true
    fi
fi

if [ "$1" = "purge" ]; then
    rm -rf /var/lib/unitill /opt/unitill/data /etc/unitill
    # Self-update (ut-docs#151) keeps a rollback .bak of the binary and, if it
    # swapped web/ assets, the old web/ dir — only present on a box that has
    # self-updated at least once. A purge must remove these too, or remnants
    # of /opt/unitill survive a full package removal (ut-docs#257).
    rm -f /opt/unitill/bin/unitill-pos.bak
    rm -rf /opt/unitill/web.bak
fi
