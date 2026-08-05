#!/usr/bin/env bash
# Regenerate the Android launcher icons from the canonical brand mark.
#
# The mipmap PNGs are derived artifacts, not authored ones — run this after
# web/public/assets/logo/unitill-logo.svg changes rather than editing them.
#
#   ./android/generate-launcher-icons.sh
#
# Requires rsvg-convert (brew install librsvg / apt install librsvg2-bin).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
canonical="$root/web/public/assets/logo/unitill-logo.svg"
res="$root/android/app/src/main/res"

command -v rsvg-convert >/dev/null || { echo "rsvg-convert not found" >&2; exit 1; }

# The mark is portrait (viewBox 0 0 14.262122 19.442995) and launcher icons are
# square, so it is re-wrapped in a square viewBox rather than stretched. Two
# insets: the square icon keeps a 12% margin; the round one uses 20% so the
# launcher's circular mask does not clip the artwork. Constants are derived as
# edge = 19.442995 / (1 - 2*margin), centred on the original box — the same
# construction as ut-docs/scripts/generate-brand-icons.py. The asset is pinned
# by hash in scripts/ci/check-brand-assets.sh, so they stay valid until the
# mark is deliberately replaced.
SQUARE_VIEWBOX="-5.6603831 -3.0699466 25.5828882 25.5828882"
ROUND_VIEWBOX="-9.0714349 -6.4809984 32.4049917 32.4049917"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

wrap() { # wrap <viewBox> <outfile>
  sed "s|viewBox=\"0 0 14.262122 19.442995\"|viewBox=\"$1\"|; s| width=\"14.262122mm\"||; s| height=\"19.442995mm\"||" \
    "$canonical" > "$2"
}
wrap "$SQUARE_VIEWBOX" "$tmp/square.svg"
wrap "$ROUND_VIEWBOX" "$tmp/round.svg"

# The mark is solid black on a transparent background; a launcher icon with no
# backing disappears against a dark wallpaper, so it is rendered onto white —
# the same plate the nav and login surfaces use.
for entry in "mdpi 48" "hdpi 72" "xhdpi 96" "xxhdpi 144" "xxxhdpi 192"; do
  set -- $entry
  density="$1" size="$2"
  rsvg-convert -w "$size" -h "$size" -b '#ffffff' "$tmp/square.svg" \
    -o "$res/mipmap-$density/ic_launcher.png"
  rsvg-convert -w "$size" -h "$size" -b '#ffffff' "$tmp/round.svg" \
    -o "$res/mipmap-$density/ic_launcher_round.png"
  echo "    mipmap-$density  ${size}x${size}"
done

echo "launcher icons regenerated from $(basename "$canonical")"
