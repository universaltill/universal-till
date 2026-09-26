#!/usr/bin/env bash
#
# ut-docs#2788: the Android till "refreshed" (whole-page reload) from a
# background event, with no operator action. Any configuration change that
# MainActivity does not declare in android:configChanges makes Android
# DESTROY and RECREATE the Activity -- and the fresh WebView loads "/", a
# full page refresh over the live sale screen. Rotation was covered years
# ago; a Bluetooth HID barcode scanner or keyboard sleeping and reconnecting
# (keyboard, navigation) and auto night mode / docking (uiMode) were not.
#
# This guard pins the full set so a manifest edit can't silently drop one,
# and enforces the precondition that makes handling uiMode safe: the app
# must ship no "-night" resource qualifiers, because with uiMode handled
# in-process nothing would ever re-apply them.
#
# Usage: guard-android-config-changes.sh [manifest] [res-dir]
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

MANIFEST="${1:-android/app/src/main/AndroidManifest.xml}"
RES_DIR="${2:-android/app/src/main/res}"
[ -f "${MANIFEST}" ] || { echo "guard-android-config-changes: ${MANIFEST} missing" >&2; exit 1; }

MANIFEST="${MANIFEST}" RES_DIR="${RES_DIR}" python3 - <<'PY'
import os
import sys
import xml.etree.ElementTree as ET

manifest_path = os.environ["MANIFEST"]
res_dir = os.environ["RES_DIR"]
ANDROID_NS = "{http://schemas.android.com/apk/res/android}"

REQUIRED = [
    # rotation (independent review, 2026-07-25)
    "orientation", "screenSize", "smallestScreenSize", "screenLayout", "keyboardHidden",
    # background-triggered (ut-docs#2788): HID scanner/keyboard reconnect,
    # auto night mode / dock.
    "keyboard", "navigation", "uiMode",
]

root = ET.parse(manifest_path).getroot()
activity = None
for el in root.iter("activity"):
    if el.get(f"{ANDROID_NS}name") in (".MainActivity", "com.universaltill.pos.MainActivity"):
        activity = el
        break
if activity is None:
    print(f"guard-android-config-changes: no MainActivity in {manifest_path}", file=sys.stderr)
    sys.exit(1)

declared = set(filter(None, (activity.get(f"{ANDROID_NS}configChanges") or "").split("|")))
missing = [v for v in REQUIRED if v not in declared]
fail = False
if missing:
    fail = True
    print(
        "guard-android-config-changes: MainActivity android:configChanges is missing "
        + "|".join(missing)
        + " -- each missing change recreates the Activity and reloads the whole "
        "till page with no operator action (ut-docs#2788).",
        file=sys.stderr,
    )

if "uiMode" in declared and os.path.isdir(res_dir):
    night = sorted(
        d for d in os.listdir(res_dir)
        if os.path.isdir(os.path.join(res_dir, d)) and "-night" in d
    )
    if night:
        fail = True
        print(
            "guard-android-config-changes: uiMode is handled in-process but "
            f"{res_dir} has night-qualified resources ({', '.join(night)}) that "
            "would then never be applied. Handle the change in "
            "onConfigurationChanged or drop the -night resources.",
            file=sys.stderr,
        )

if fail:
    sys.exit(1)
print(f"guard-android-config-changes: OK ({manifest_path})")
PY
