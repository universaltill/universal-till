#!/usr/bin/env bash
#
# The kiosk launch script must never regress on flags that avoid blocking,
# undismissable GTK dialogs on a touchscreen-only kiosk with no keyboard/mouse
# to interact with them. --password-store=basic avoids a real, confirmed-live
# "keyring is locked" prompt on Chromium's first password-manager touch
# (2026-07-29) -- there is no way to dismiss it from a bare touchscreen.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/packaging/linux/unitill-kiosk-launch.sh"

if ! grep -q -- '--password-store=basic' "$SCRIPT"; then
  echo "❌ kiosk-launch guard: ${SCRIPT} is missing --password-store=basic" >&2
  echo "   (without it, Chromium can block the kiosk behind an undismissable keyring prompt)" >&2
  exit 1
fi

echo "✓ kiosk-launch guard: --password-store=basic present"
