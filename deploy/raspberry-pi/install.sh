#!/usr/bin/env bash
# Installs the Universal Till POS as an auto-starting fullscreen kiosk on a
# Raspberry Pi (Raspberry Pi OS with desktop, user autologin recommended).
#
# Run from a checkout on the Pi (or copy a prebuilt arm64 binary to bin/):
#   sudo deploy/raspberry-pi/install.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DEST=/opt/unitill

if [[ $EUID -ne 0 ]]; then echo "run with sudo" >&2; exit 1; fi

echo "==> Creating pos user + directories"
id -u pos >/dev/null 2>&1 || useradd --system --create-home --shell /usr/sbin/nologin pos
mkdir -p "$DEST/bin" "$DEST/data"

echo "==> Building (or copy your own arm64 binary to $DEST/bin/unitill-pos)"
if command -v go >/dev/null 2>&1; then
  (cd "$REPO_ROOT" && go build -trimpath -ldflags="-s -w" -o "$DEST/bin/unitill-pos" .)
else
  cp "$REPO_ROOT/bin/unitill-pos" "$DEST/bin/unitill-pos"
fi

echo "==> Installing web assets + env"
cp -R "$REPO_ROOT/web" "$DEST/web"
[[ -f "$DEST/pos.env" ]] || cp "$REPO_ROOT/pos.env.dev" "$DEST/pos.env"
chown -R pos:pos "$DEST"

echo "==> Installing systemd units"
cp "$REPO_ROOT/deploy/raspberry-pi/unitill-pos.service" /etc/systemd/system/
cp "$REPO_ROOT/deploy/raspberry-pi/unitill-kiosk.service" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now unitill-pos.service
systemctl enable unitill-kiosk.service

echo ""
echo "Done. The POS starts on boot; the kiosk browser starts with the desktop"
echo "session. Reboot to see the full flow, or: systemctl start unitill-kiosk"
echo "Edit /opt/unitill/pos.env for marketplace credentials/endpoints."
