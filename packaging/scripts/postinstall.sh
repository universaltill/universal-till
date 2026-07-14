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

chown -R pos:pos /opt/unitill/data /var/lib/unitill
# The till writes AI reference photos next to items too; keep the whole
# assets dir writable for the service user.
chown -R pos:pos /opt/unitill/web/public/assets 2>/dev/null || true

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload
    systemctl enable unitill-pos.service >/dev/null 2>&1 || true
    # Start (or restart after upgrade) — never fail the install on it.
    systemctl restart unitill-pos.service >/dev/null 2>&1 || true
fi

echo "Universal Till installed. Open http://localhost:8080 — the first-boot"
echo "wizard sets language, currency and the admin PIN."
echo "Config: /opt/unitill/pos.env · Data: /opt/unitill/data"
