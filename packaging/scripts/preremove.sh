#!/bin/sh
# Stop the service on package removal; data in /opt/unitill/data and
# /var/lib/unitill is deliberately left in place.
set -e
if [ -d /run/systemd/system ]; then
    systemctl disable --now unitill-pos.service >/dev/null 2>&1 || true
fi
