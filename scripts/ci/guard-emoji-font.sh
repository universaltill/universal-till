#!/usr/bin/env bash
#
# The POS UI renders icons (menu tiles, status glyphs, plugin buttons) as
# plain Unicode emoji text across ~25 templates, not image/SVG assets. A
# field report (2026-07-29) found menu icons invisible on a real Raspberry
# Pi kiosk: the base Pi OS image ships no color-emoji font, and the app's
# CSS font stack had no emoji-capable fallback, so the glyphs rendered as
# blank/tofu boxes. Guards against silently regressing any of the three
# halves of the fix: the CSS fallback (helps any platform that already has
# one of these fonts, e.g. Windows/most desktop Linux) and a color-emoji
# font package deterministically installed by EVERY Linux install path --
# the .deb's own dependency list and the kiosk setup script (the old
# curl-pipe deploy/raspberry-pi install script was retired with ut-docs#6).
# The kiosk one matters most: its documented install
# flow is `apt install --no-install-recommends ./unitill-pos_*.deb`
# (see packaging/linux/unitill-kiosk-setup.sh header), which deliberately
# bypasses the .deb's `recommends` -- so the font must ALSO be installed
# explicitly there, or the exact field-reported configuration (Pi kiosk)
# stays broken while every other path is fixed.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

CSS="web/public/app.css"
GORELEASER=".goreleaser.yaml"
KIOSK_SETUP="packaging/linux/unitill-kiosk-setup.sh"

if ! grep -qE 'Noto Color Emoji|Segoe UI Emoji|Apple Color Emoji' "${CSS}"; then
  echo "❌ emoji-font guard: ${CSS} has no emoji-capable font fallback" >&2
  echo "   (menu/status icons are plain emoji glyphs -- without a fallback font name" >&2
  echo "   in the font-family stack, they render as blank boxes on platforms with no" >&2
  echo "   system color-emoji font, e.g. a base Raspberry Pi OS image)" >&2
  exit 1
fi

# Anchored to the actual YAML list-item form: the recommends block also has
# an explanatory comment containing the package name, and a bare substring
# grep passed with the real entry deleted (found by the independent review).
if ! grep -qE '^[[:space:]]*-[[:space:]]*fonts-noto-color-emoji[[:space:]]*$' "${GORELEASER}"; then
  echo "❌ emoji-font guard: ${GORELEASER} no longer lists fonts-noto-color-emoji in the .deb's recommends" >&2
  echo "   (a plain \`apt install ./unitill-pos_*.deb\` on a base Raspberry Pi OS image would" >&2
  echo "   leave the system with no color-emoji font -- menu/status icons render as blank boxes)" >&2
  exit 1
fi

if ! grep -qE "^[^#\"']*apt-get[^#\"']* install[^#\"']*fonts-noto-color-emoji" "${KIOSK_SETUP}"; then
  echo "❌ emoji-font guard: ${KIOSK_SETUP} no longer installs fonts-noto-color-emoji" >&2
  echo "   (the kiosk flow's documented install command uses --no-install-recommends, which" >&2
  echo "   bypasses the .deb's recommends list entirely -- the kiosk setup script must install" >&2
  echo "   the font explicitly, or the exact field-reported Pi-kiosk configuration stays broken)" >&2
  exit 1
fi

echo "✓ emoji-font guard: CSS fallback present; both Linux install paths provide fonts-noto-color-emoji"
