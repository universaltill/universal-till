#!/bin/sh
# On PURGE (apt remove --purge), delete the shop data too. A plain removal
# (apt remove) deliberately keeps /var/lib/unitill and /opt/unitill/data so a
# reinstall picks up the existing database. dpkg passes the action as $1.
set -e
if [ "$1" = "purge" ]; then
    rm -rf /var/lib/unitill /opt/unitill/data
fi
