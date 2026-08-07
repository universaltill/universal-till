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

# ut-docs#395: Wayland screen capture goes through xdg-desktop-portal, which is
# D-Bus-activated and therefore does NOT inherit this script's environment. If
# XDG_CURRENT_DESKTOP is not pushed into the activation environment, the portal
# cannot select the wlr backend and Chromium's getDisplayMedia picker opens with
# nothing to select — the bug reporter's screenshot/recording is then unusable
# on a kiosk, which is exactly how it shipped. Both lines are load-bearing:
# exporting it alone is not enough.
if ! grep -q 'XDG_CURRENT_DESKTOP=wlroots' "$SCRIPT"; then
  echo "❌ kiosk-launch guard: ${SCRIPT} does not set XDG_CURRENT_DESKTOP=wlroots" >&2
  echo "   (without it xdg-desktop-portal cannot pick the wlr backend, and" >&2
  echo "    screen capture silently has no sources — ut-docs#395)" >&2
  exit 1
fi
if ! grep -q 'dbus-update-activation-environment' "$SCRIPT"; then
  echo "❌ kiosk-launch guard: ${SCRIPT} never pushes the session environment" >&2
  echo "   into the D-Bus activation environment. The portal is D-Bus-activated," >&2
  echo "   so exporting XDG_CURRENT_DESKTOP alone does not reach it (ut-docs#395)." >&2
  exit 1
fi

echo "✓ kiosk-launch guard: screencast portal environment wired"
