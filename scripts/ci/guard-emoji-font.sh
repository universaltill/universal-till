#!/usr/bin/env bash
#
# The POS UI renders icons (menu tiles, status glyphs, plugin buttons) as
# plain Unicode emoji text across ~25 templates, not image/SVG assets. A
# field report (2026-07-29) found menu icons invisible on a real Raspberry
# Pi kiosk: the base Pi OS image ships no color-emoji font, and the app's
# CSS font stack had no emoji-capable fallback, so the glyphs rendered as
# blank/tofu boxes. Guards against silently regressing any of the four
# halves of the fix: the CSS fallback (helps any platform that already has
# one of these fonts, e.g. Windows/most desktop Linux) and a color-emoji
# font package deterministically installed by EVERY Linux install path --
# the curl-pipe Pi install script, the .deb's own dependency list, and the
# kiosk setup script. The kiosk one matters most: its documented install
# flow is `apt install --no-install-recommends ./unitill-pos_*.deb`
# (see packaging/linux/unitill-kiosk-setup.sh header), which deliberately
# bypasses the .deb's `recommends` -- so the font must ALSO be installed
# explicitly there, or the exact field-reported configuration (Pi kiosk)
# stays broken while every other path is fixed.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

CSS="web/public/app.css"
INSTALL_SH="deploy/raspberry-pi/install.sh"
GORELEASER=".goreleaser.yaml"
KIOSK_SETUP="packaging/linux/unitill-kiosk-setup.sh"

if ! grep -qE 'Noto Color Emoji|Segoe UI Emoji|Apple Color Emoji' "${CSS}"; then
  echo "❌ emoji-font guard: ${CSS} has no emoji-capable font fallback" >&2
  echo "   (menu/status icons are plain emoji glyphs -- without a fallback font name" >&2
  echo "   in the font-family stack, they render as blank boxes on platforms with no" >&2
  echo "   system color-emoji font, e.g. a base Raspberry Pi OS image)" >&2
  exit 1
fi

# Anchored to a real apt install line: not commented out (#) and not inside
# a quoted string (the script's own failure-hint echo says "sudo apt-get
# install fonts-noto-color-emoji", which a looser match would count). A bare
# substring match false-passing on a mere mention is exactly what the
# independent review demonstrated against the goreleaser check.
if ! grep -qE '^[^#"'"'"']*apt-get install[^#"'"'"']*fonts-noto-color-emoji' "${INSTALL_SH}"; then
  echo "❌ emoji-font guard: ${INSTALL_SH} no longer installs fonts-noto-color-emoji" >&2
  echo "   (the real field-reported device needs this installed explicitly --" >&2
  echo "   a base Raspberry Pi OS image doesn't ship a color-emoji font by default)" >&2
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

if ! grep -qE '^[^#"'"'"']*apt-get install[^#"'"'"']*fonts-noto-color-emoji' "${KIOSK_SETUP}"; then
  echo "❌ emoji-font guard: ${KIOSK_SETUP} no longer installs fonts-noto-color-emoji" >&2
  echo "   (the kiosk flow's documented install command uses --no-install-recommends, which" >&2
  echo "   bypasses the .deb's recommends list entirely -- the kiosk setup script must install" >&2
  echo "   the font explicitly, or the exact field-reported Pi-kiosk configuration stays broken)" >&2
  exit 1
fi

echo "✓ emoji-font guard: CSS fallback present; all three Linux install paths provide fonts-noto-color-emoji"
