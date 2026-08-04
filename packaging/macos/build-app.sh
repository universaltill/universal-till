#!/usr/bin/env bash
# Build "Universal Till.app" — a real macOS app bundle that runs the POS server
# and shows it in an embedded WebView window (no browser chrome, no Terminal).
#
# The bundle is AD-HOC codesigned (`codesign --sign -`). That is REQUIRED for
# Apple Silicon to run it at all (unsigned arm64 binaries are killed on launch);
# it is NOT Apple notarization, so a *downloaded* copy is still quarantined and
# Gatekeeper asks the user to right-click → Open once (or run the bundled
# "Remove quarantine" helper). Proper notarization needs a paid Apple Developer
# account — see docs/arch/desktop-app.md.
#
# Usage:  packaging/macos/build-app.sh [version] [output-dir]
# Needs:  macOS with Xcode CLT (clang for CGO), Go, iconutil/sips (built in).
set -euo pipefail

VERSION="${1:-dev}"
OUTDIR="${2:-dist/macos}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

APP="$OUTDIR/Universal Till.app"
LDFLAGS="-s -w -X github.com/universaltill/universal-till/internal/buildinfo.Version=${VERSION}"

echo "==> building binaries (arm64)"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

# The pure-Go server (same binary the tar/deb ship).
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  go build -ldflags "$LDFLAGS" -o "$APP/Contents/MacOS/unitill-pos" .

# The WebView shell (CGO + system WKWebView, behind the `desktop` build tag).
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
  go build -tags desktop -ldflags "$LDFLAGS" -o "$APP/Contents/MacOS/unitill-desktop" ./cmd/unitill-desktop

echo "==> staging web assets"
# web/ is a resource, not code, so it goes in Contents/Resources (codesign
# rejects non-code under MacOS/). The desktop shell points the server's working
# dir here. Data (DB, backups, plugins) lives outside the read-only bundle in
# ~/Library/Application Support/UniversalTill (internal/paths default).
cp -R web "$APP/Contents/Resources/web"

# Runtime config: main.go loads pos.env from the working dir (Contents/Resources
# here). Without it the marketplace endpoint falls back to the dev default
# 127.0.0.1:8081 and store registration / the plugin store can never reach the
# marketplace. Ship the production endpoint so a fresh app enrols out of the box.
cp packaging/pos.env.example "$APP/Contents/Resources/pos.env"

echo "==> Info.plist"
sed "s/__VERSION__/${VERSION}/g" packaging/macos/Info.plist > "$APP/Contents/Info.plist"

echo "==> app icon"
ICONSET="$(mktemp -d)/AppIcon.iconset"
mkdir -p "$ICONSET"
BASE_PNG="$(mktemp -d)/icon.png"
# Rasterize the vector logo to a 1024px master, then derive every icon size.
if qlmanage -t -s 1024 -o "$(dirname "$BASE_PNG")" web/public/assets/logo/unitill-logo.svg >/dev/null 2>&1; then
  mv "$(dirname "$BASE_PNG")"/unitill-logo.svg.png "$BASE_PNG"
fi
if [ -f "$BASE_PNG" ]; then
  for s in 16 32 64 128 256 512 1024; do
    sips -z "$s" "$s" "$BASE_PNG" --out "$ICONSET/icon_${s}x${s}.png" >/dev/null 2>&1 || true
  done
  # Retina (@2x) variants Apple expects.
  cp "$ICONSET/icon_32x32.png"   "$ICONSET/icon_16x16@2x.png"   2>/dev/null || true
  cp "$ICONSET/icon_64x64.png"   "$ICONSET/icon_32x32@2x.png"   2>/dev/null || true
  cp "$ICONSET/icon_256x256.png" "$ICONSET/icon_128x128@2x.png" 2>/dev/null || true
  cp "$ICONSET/icon_512x512.png" "$ICONSET/icon_256x256@2x.png" 2>/dev/null || true
  cp "$ICONSET/icon_1024x1024.png" "$ICONSET/icon_512x512@2x.png" 2>/dev/null || true
  iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/AppIcon.icns" 2>/dev/null \
    && echo "    icon embedded" || echo "    (icon skipped)"
else
  echo "    (no icon source; skipping)"
fi

# Codesign. With a real Developer ID identity (MACOS_SIGN_IDENTITY set, e.g.
# "Developer ID Application: Task Runner Technology LTD (TEAMID)") we sign with
# the hardened runtime + a secure timestamp, which is what notarization
# requires. Without it we ad-hoc sign (`--sign -`): free, and required for
# Apple Silicon to run the app at all, but a downloaded copy still hits
# Gatekeeper (right-click → Open). See docs/arch/desktop-app.md.
if [ -n "${MACOS_SIGN_IDENTITY:-}" ]; then
  echo "==> codesign with Developer ID: $MACOS_SIGN_IDENTITY"
  SIGN_ARGS=(--force --options runtime --timestamp --sign "$MACOS_SIGN_IDENTITY")
else
  echo "==> ad-hoc codesign (no MACOS_SIGN_IDENTITY set)"
  SIGN_ARGS=(--force --timestamp=none --sign -)
fi
# Sign inner binaries first, then the bundle, so --verify passes.
codesign "${SIGN_ARGS[@]}" "$APP/Contents/MacOS/unitill-pos"
codesign "${SIGN_ARGS[@]}" "$APP/Contents/MacOS/unitill-desktop"
codesign "${SIGN_ARGS[@]}" --deep "$APP"
codesign --verify --deep --strict "$APP" && echo "    signature valid"

echo
echo "Built: $APP"
if [ -z "${MACOS_SIGN_IDENTITY:-}" ]; then
  echo "NOTE: ad-hoc signed — downloaded copies need right-click → Open. Set"
  echo "      MACOS_SIGN_IDENTITY (+ notarize in make-dmg.sh) for a clean open."
fi
echo "Run locally:  open \"$APP\""
echo "Distribute:   packaging/macos/make-dmg.sh \"$VERSION\" \"$OUTDIR\""
