#!/usr/bin/env bash
# Package "Universal Till.app" into a distributable .dmg with a drag-to-
# Applications layout and a one-click "Open Universal Till" helper that clears
# the download quarantine (for an ad-hoc fork/local build: macOS 15+ otherwise
# needs System Settings → Privacy & Security → Open Anyway; release builds are
# Developer ID signed + notarized).
#
# MACOS_REQUIRE_NOTARIZED=1 (set by release.yml upstream, ut-docs#6025) turns
# an ad-hoc or un-notarized result into a hard failure.
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
if [ "${MACOS_REQUIRE_NOTARIZED:-}" = 1 ] && [ -z "${MACOS_SIGN_IDENTITY:-}" ]; then
  echo "::error::MACOS_REQUIRE_NOTARIZED=1 but MACOS_SIGN_IDENTITY is empty — refusing an ad-hoc release .dmg (ut-docs#6025)" >&2
  exit 1
fi

STAGE="$(mktemp -d)/dmg"
mkdir -p "$STAGE"
cp -R "$APP" "$STAGE/"
ln -s /Applications "$STAGE/Applications"

# First-run helper: double-clicking it de-quarantines and opens the app, so a
# quarantined ad-hoc app opens without the Open Anyway trip through System
# Settings.
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
# Developer ID signed — ad-hoc apps cannot be notarized. Credentials are picked
# by notary-args.sh: an App Store Connect API key (what CI uses, ut-docs#2870),
# a keychain profile, or Apple ID creds. See docs/arch/desktop-app.md.
if [ -n "${MACOS_SIGN_IDENTITY:-}" ]; then
  # The disk image is itself signed with the Developer ID (ut-docs#2870):
  # Gatekeeper assesses a .dmg by its own signature, and v0.25.0's accepted,
  # stapled but unsigned image failed `spctl --type open` with "no usable
  # signature". Apple's flow: create .dmg → codesign → notarize → staple.
  echo "==> codesign the disk image"
  codesign --force --timestamp --sign "$MACOS_SIGN_IDENTITY" "$DMG"
  codesign --verify --strict "$DMG"
  # shellcheck source=packaging/macos/notary-args.sh
  . "$(dirname "$0")/notary-args.sh"
  # Trap first: notary_args writes the .p8 to disk, and a failure between
  # that write and a later trap would leave it behind.
  trap notary_cleanup EXIT
  notary_args
  if [ "${#NOTARY[@]}" -gt 0 ]; then
    # Bounded (ut-docs#2917): fails the build on timeout/rejection, so an
    # un-notarized .dmg is never produced as a release candidate.
    notary_submit_and_wait "$DMG"
    notary_cleanup
    echo "==> stapling"
    xcrun stapler staple "$APP"        # so the app validates offline once copied out
    xcrun stapler staple "$DMG"
    xcrun stapler validate "$DMG"
    # Fails the build if Gatekeeper would not accept the notarized image.
    spctl --assess --type open --context context:primary-signature -v "$DMG"
  elif [ "${MACOS_REQUIRE_NOTARIZED:-}" = 1 ]; then
    echo "::error::MACOS_REQUIRE_NOTARIZED=1 but no complete notary credentials — refusing an un-notarized release .dmg (ut-docs#6025)" >&2
    exit 1
  else
    echo "==> signed but not notarized (no notary credentials); Gatekeeper will still prompt"
  fi
fi
echo "Built: $DMG"
