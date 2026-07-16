#!/usr/bin/env bash
# Package "Universal Till.app" into a distributable .dmg with a drag-to-
# Applications layout and a one-click "Open Universal Till" helper that clears
# the download quarantine (so an unsigned/ad-hoc app opens without the
# right-click dance).
#
# Usage:  packaging/macos/make-dmg.sh [version] [dist-dir]
# Assumes build-app.sh already produced "$DIST/Universal Till.app".
set -euo pipefail

VERSION="${1:-dev}"
DIST="${2:-dist/macos}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

APP="$DIST/Universal Till.app"
[ -d "$APP" ] || { echo "no app at $APP — run build-app.sh first" >&2; exit 1; }

STAGE="$(mktemp -d)/dmg"
mkdir -p "$STAGE"
cp -R "$APP" "$STAGE/"
ln -s /Applications "$STAGE/Applications"

# First-run helper: double-clicking it de-quarantines and opens the app, for
# users who don't know the right-click → Open trick.
cat > "$STAGE/Open Universal Till.command" <<'SH'
#!/bin/bash
cd "$(dirname "$0")"
xattr -dr com.apple.quarantine "/Applications/Universal Till.app" 2>/dev/null || true
open "/Applications/Universal Till.app"
SH
chmod +x "$STAGE/Open Universal Till.command"

DMG="$DIST/unitill-pos-${VERSION}-macOS-arm64.dmg"
rm -f "$DMG"
echo "==> building $DMG"
hdiutil create -volname "Universal Till" -srcfolder "$STAGE" -ov -format UDZO "$DMG" >/dev/null
echo "Built: $DMG"
