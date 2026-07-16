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

# Uninstaller: quits the app, removes it from /Applications, and asks before
# deleting shop data. Ships in the .dmg so removal is one double-click too.
cat > "$STAGE/Uninstall Universal Till.command" <<'SH'
#!/bin/bash
DATA_DIR="$HOME/Library/Application Support/UniversalTill"
echo "Universal Till - uninstall"
echo
osascript -e 'quit app "Universal Till"' 2>/dev/null || true
pkill -x unitill-pos 2>/dev/null || true
sleep 1
if [ -d "/Applications/Universal Till.app" ]; then
  rm -rf "/Applications/Universal Till.app"
  echo "Removed /Applications/Universal Till.app"
else
  echo "App not found in /Applications (already removed?)."
fi
echo
if [ -d "$DATA_DIR" ]; then
  echo "Your shop data (database, plugins, backups) is at:"
  echo "  $DATA_DIR"
  printf "Delete ALL shop data too? This cannot be undone. [y/N] "
  read -r reply
  case "$reply" in
    [yY]|[yY][eE][sS]) rm -rf "$DATA_DIR"; echo "Shop data deleted." ;;
    *) echo "Kept your shop data at $DATA_DIR" ;;
  esac
fi
echo
echo "Done. You can now eject this disk image."
read -r -p "Press Return to close." _
SH
chmod +x "$STAGE/Uninstall Universal Till.command"

DMG="$DIST/unitill-pos-${VERSION}-macOS-arm64.dmg"
rm -f "$DMG"
echo "==> building $DMG"
hdiutil create -volname "Universal Till" -srcfolder "$STAGE" -ov -format UDZO "$DMG" >/dev/null

# Notarize + staple so the app opens with a plain double-click (no Gatekeeper
# prompt). Runs only when notarytool credentials are provided and the app was
# Developer ID signed — ad-hoc apps cannot be notarized. Provide EITHER a stored
# keychain profile (MACOS_NOTARY_PROFILE) OR Apple ID creds
# (MACOS_NOTARY_APPLE_ID + MACOS_NOTARY_TEAM_ID + MACOS_NOTARY_PASSWORD, an
# app-specific password). See docs/arch/desktop-app.md.
if [ -n "${MACOS_SIGN_IDENTITY:-}" ]; then
  if [ -n "${MACOS_NOTARY_PROFILE:-}" ]; then
    NOTARY=(--keychain-profile "$MACOS_NOTARY_PROFILE")
  elif [ -n "${MACOS_NOTARY_APPLE_ID:-}" ] && [ -n "${MACOS_NOTARY_TEAM_ID:-}" ] && [ -n "${MACOS_NOTARY_PASSWORD:-}" ]; then
    NOTARY=(--apple-id "$MACOS_NOTARY_APPLE_ID" --team-id "$MACOS_NOTARY_TEAM_ID" --password "$MACOS_NOTARY_PASSWORD")
  fi
  if [ -n "${NOTARY:-}" ]; then
    echo "==> notarizing (this can take a few minutes)"
    xcrun notarytool submit "$DMG" "${NOTARY[@]}" --wait
    echo "==> stapling"
    xcrun stapler staple "$APP"        # so the app validates offline once copied out
    xcrun stapler staple "$DMG"
    spctl --assess --type open --context context:primary-signature -v "$DMG" || true
  else
    echo "==> signed but not notarized (no notary credentials); Gatekeeper will still prompt"
  fi
fi
echo "Built: $DMG"
