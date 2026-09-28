#!/usr/bin/env bash
#
# Writes the iOS/iPadOS app icon asset catalog (ut-docs#3069) from the
# canonical brand mark, web/public/assets/logo/unitill-logo.svg:
#
#   <out>/Contents.json
#   <out>/AppIcon.appiconset/Contents.json   one universal iOS 1024x1024 entry
#   <out>/AppIcon.appiconset/AppIcon-1024.png
#
# <out> defaults to ios/UniversalTill/Assets.xcassets (git-ignored; run this
# before `xcodegen generate`). Xcode derives every smaller size from the
# single 1024 image (Xcode 14+ "single size" app icons).
#
# The PNG is the dark mark centred on a white tile — the same convention as
# the Android launcher icon (android/app/src/main/res/mipmap-*) — the mark
# 70% of the tile's height, leaving room for iOS's rounded-corner mask. It is
# fully OPAQUE RGB: App Store Connect rejects an app icon with an alpha
# channel. Built-in macOS tools only (qlmanage + sips), like
# packaging/macos/build-app.sh, so it runs on a bare GitHub macos-15 runner.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SRC="${ROOT_DIR}/web/public/assets/logo/unitill-logo.svg"
OUT="${1:-${ROOT_DIR}/ios/UniversalTill/Assets.xcassets}"

for tool in qlmanage sips; do
  command -v "$tool" >/dev/null 2>&1 || { echo "make-app-icon: ${tool} not found (needs macOS)" >&2; exit 1; }
done
test -s "$SRC" || { echo "make-app-icon: ${SRC} missing" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The mark's viewBox is portrait (0 0 14.262122 19.442995). Re-wrap it in a
# square viewBox so the mark is 70% of the tile's height, centred:
#   side = 19.442995 / 0.70 = 27.7757071
#   x0   = -(side - 14.262122) / 2 = -6.7567926
#   y0   = -(side - 19.442995) / 2 = -4.1663561
# and paint a white square under it (the tile). The canonical asset is
# pinned by hash in scripts/ci/check-brand-assets.sh, so the viewBox this
# sed matches stays valid until the mark is deliberately replaced; the
# grep below fails loudly if it ever doesn't.
grep -qF 'viewBox="0 0 14.262122 19.442995"' "$SRC" \
  || { echo "make-app-icon: canonical logo viewBox changed; update the square-wrap constants" >&2; exit 1; }
square="${work}/app-icon.svg"
sed -e 's|viewBox="0 0 14.262122 19.442995"|viewBox="-6.7567926 -4.1663561 27.7757071 27.7757071"|' \
    -e 's| width="14.262122mm"| width="1024"|; s| height="19.442995mm"| height="1024"|' \
    -e 's|</defs>|</defs><rect x="-6.7567926" y="-4.1663561" width="27.7757071" height="27.7757071" fill="#ffffff"/>|' \
    "$SRC" >"$square"
grep -qF '<rect x="-6.7567926"' "$square" \
  || { echo "make-app-icon: could not insert the background tile" >&2; exit 1; }

# Rasterize (Quick Look renders SVG with WebKit) at 1024px.
qlmanage -t -s 1024 -o "$work" "$square" >/dev/null 2>&1 || true
raster="${work}/app-icon.svg.png"
test -s "$raster" || { echo "make-app-icon: qlmanage did not rasterize ${square}" >&2; exit 1; }

# Drop the alpha channel: Quick Look writes RGBA even for an opaque image.
# A round trip through JPEG (no alpha by definition) at maximum quality
# flattens it; the tile is already white, so nothing is composited.
flat="${work}/flat.jpg"
sips -s format jpeg -s formatOptions best -z 1024 1024 "$raster" --out "$flat" >/dev/null
mkdir -p "${OUT}/AppIcon.appiconset"
png="${OUT}/AppIcon.appiconset/AppIcon-1024.png"
sips -s format png "$flat" --out "$png" >/dev/null

cat >"${OUT}/Contents.json" <<'JSON'
{
  "info" : {
    "author" : "xcode",
    "version" : 1
  }
}
JSON
cat >"${OUT}/AppIcon.appiconset/Contents.json" <<'JSON'
{
  "images" : [
    {
      "filename" : "AppIcon-1024.png",
      "idiom" : "universal",
      "platform" : "ios",
      "size" : "1024x1024"
    }
  ],
  "info" : {
    "author" : "xcode",
    "version" : 1
  }
}
JSON

alpha="$(sips -g hasAlpha "$png")"
if grep -qF 'hasAlpha: yes' <<<"$alpha"; then
  echo "make-app-icon: ${png} still has an alpha channel" >&2
  exit 1
fi
echo "make-app-icon: wrote ${OUT}"
