#!/usr/bin/env bash
#
# ut-docs#3088: with Wi-Fi turned off, the Android till's status bar kept
# showing "Online" with a green dot. That light is navigator.onLine
# (web/ui/layouts/base.html, #sb-conn), and an Android WebView only ever
# changes navigator.onLine -- and fires the online/offline events the page
# listens for -- when the app tells it to with WebView.setNetworkAvailable().
# The app never did, and it did not hold ACCESS_NETWORK_STATE, the
# permission it needs to watch the network at all. So the light was stuck
# on "online" for the life of the WebView.
#
# This guard keeps both halves in place:
#   1. the manifest declares android.permission.ACCESS_NETWORK_STATE
#      (a normal, install-time permission: no prompt, no implied feature);
#   2. MainActivity calls setNetworkAvailable( outside a comment.
#
# Neither regression would show anywhere but on a real device with its
# network switched off, which no CI job has.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

MANIFEST="${1:-android/app/src/main/AndroidManifest.xml}"
ACTIVITY="${2:-android/app/src/main/java/com/universaltill/pos/MainActivity.kt}"
[ -f "${MANIFEST}" ] || { echo "guard-android-network-state: ${MANIFEST} missing" >&2; exit 1; }
[ -f "${ACTIVITY}" ] || { echo "guard-android-network-state: ${ACTIVITY} missing" >&2; exit 1; }

MANIFEST="${MANIFEST}" ACTIVITY="${ACTIVITY}" python3 - <<'PY'
import os
import re
import sys
import xml.etree.ElementTree as ET

ANDROID_NS = "{http://schemas.android.com/apk/res/android}"
manifest_path = os.environ["MANIFEST"]
activity_path = os.environ["ACTIVITY"]

root = ET.parse(manifest_path).getroot()
declared = {el.get(f"{ANDROID_NS}name") for el in root.findall("uses-permission")}

fail = False
if "android.permission.ACCESS_NETWORK_STATE" not in declared:
    fail = True
    print(
        f"guard-android-network-state: {manifest_path} does not declare "
        "android.permission.ACCESS_NETWORK_STATE -- without it the app cannot "
        "watch the network, and the WebView's navigator.onLine never turns "
        "offline (ut-docs#3088).",
        file=sys.stderr,
    )

# Strip /* */ block comments (KDoc included), then // line comments -- a
# "//" at line start or after whitespace, so "http://x" in a string stays --
# so a call that only survives in a comment does not count. The call must
# be on a receiver (".setNetworkAvailable("): a local function that merely
# shares the name is not a call to the WebView.
with open(activity_path, encoding="utf-8") as f:
    src = f.read()
src = re.sub(r"/\*.*?\*/", "", src, flags=re.S)
code = "\n".join(re.sub(r"(^|\s)//.*$", "", line) for line in src.splitlines())
if not re.search(r"\.setNetworkAvailable\s*\(", code):
    fail = True
    print(
        f"guard-android-network-state: {activity_path} never calls "
        "WebView.setNetworkAvailable( -- the page's online/offline light then "
        "stays on 'online' with the network off (ut-docs#3088).",
        file=sys.stderr,
    )

if fail:
    sys.exit(1)
print(f"guard-android-network-state: OK ({manifest_path}, {activity_path})")
PY
