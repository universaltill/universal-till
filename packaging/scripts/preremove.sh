#!/bin/sh
# Stop the service on package removal; data in /opt/unitill/data and
# /var/lib/unitill is deliberately left in place.
set -e
if [ -d /run/systemd/system ]; then
    systemctl disable --now unitill-pos.service >/dev/null 2>&1 || true
    # Kiosk units are written at install/setup time (not package-owned), so
    # dpkg won't stop them — do it here, or cage restart-loops on tty1
    # against a binary that's about to disappear and getty never comes back.
    systemctl disable --now unitill-kiosk.service >/dev/null 2>&1 || true
    systemctl disable --now unitill-kiosk-firstboot.service >/dev/null 2>&1 || true
fi
