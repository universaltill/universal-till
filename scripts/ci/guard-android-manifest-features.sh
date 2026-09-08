#!/usr/bin/env bash
#
# ut-docs#1770: AndroidManifest.xml declared android.permission.CAMERA with
# no matching <uses-feature android:name="android.hardware.camera">. That
# reads like an omission that keeps the till installable on camera-less
# hardware, but it is backwards: CAMERA is on Android's "permissions that
# imply feature requirements" list, so the Play Store DERIVES
# android.hardware.camera with required="true" from the permission alone
# when the element is missing -- the exact opposite of the intent. Only an
# explicit <uses-feature required="false"> says "use it if present". The
# same class of bug was already found and fixed for the Bluetooth
# permissions in ut-docs#1751; this guard makes sure it can't come back
# there, on the camera/microphone/location fix that closed ut-docs#1770, or
# on any future permission this manifest adds from the same list.
#
# The failure this catches is invisible in every environment that matters
# during development: a sideloaded APK (every till running today) never
# consults <uses-feature> at all, and CI has no Play Console to submit to --
# so a regression here would ship silently and only surface as a store
# rejection on hardware lacking the sensor, exactly the scenario this issue
# was filed from.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

MANIFEST="${1:-android/app/src/main/AndroidManifest.xml}"
[ -f "${MANIFEST}" ] || { echo "guard-android-manifest-features: ${MANIFEST} missing" >&2; exit 1; }

MANIFEST="${MANIFEST}" python3 - <<'PY'
import os
import sys
import xml.etree.ElementTree as ET

manifest_path = os.environ["MANIFEST"]
ANDROID_NS = "{http://schemas.android.com/apk/res/android}"

# Android's own "permissions that imply feature requirements" table
# (developer.android.com/guide/topics/manifest/uses-feature-element).
# Only permissions this app could plausibly ever declare are listed here;
# extend this map, not a per-call exception, if a new one is added.
IMPLIED_FEATURES = {
    "android.permission.CAMERA": ["android.hardware.camera"],
    "android.permission.RECORD_AUDIO": ["android.hardware.microphone"],
    "android.permission.ACCESS_COARSE_LOCATION": ["android.hardware.location"],
    "android.permission.ACCESS_FINE_LOCATION": [
        "android.hardware.location",
        "android.hardware.location.gps",
    ],
    "android.permission.BLUETOOTH": ["android.hardware.bluetooth"],
    "android.permission.BLUETOOTH_ADMIN": ["android.hardware.bluetooth"],
    "android.permission.CALL_PHONE": ["android.hardware.telephony"],
    "android.permission.CALL_PRIVILEGED": ["android.hardware.telephony"],
    "android.permission.PROCESS_OUTGOING_CALLS": ["android.hardware.telephony"],
    "android.permission.READ_SMS": ["android.hardware.telephony"],
    "android.permission.RECEIVE_SMS": ["android.hardware.telephony"],
    "android.permission.RECEIVE_MMS": ["android.hardware.telephony"],
    "android.permission.RECEIVE_WAP_PUSH": ["android.hardware.telephony"],
    "android.permission.SEND_SMS": ["android.hardware.telephony"],
    "android.permission.WRITE_APN_SETTINGS": ["android.hardware.telephony"],
    "android.permission.CHANGE_WIFI_STATE": ["android.hardware.wifi"],
    "android.permission.ACCESS_WIFI_STATE": ["android.hardware.wifi"],
    "android.permission.CHANGE_WIFI_MULTICAST_STATE": ["android.hardware.wifi"],
}

tree = ET.parse(manifest_path)
root = tree.getroot()

declared_permissions = {
    el.get(f"{ANDROID_NS}name")
    for el in root.findall("uses-permission")
    if el.get(f"{ANDROID_NS}name")
}

# A <uses-feature required="false"> element for a given feature name --
# required defaults to "true" per the Android schema, so an element present
# without an explicit required="false" does NOT satisfy this check.
optional_features = {
    el.get(f"{ANDROID_NS}name")
    for el in root.findall("uses-feature")
    if el.get(f"{ANDROID_NS}name")
    and el.get(f"{ANDROID_NS}required") == "false"
}

fail = False
for permission in sorted(declared_permissions):
    for feature in IMPLIED_FEATURES.get(permission, []):
        if feature not in optional_features:
            fail = True
            print(
                f"guard-android-manifest-features: {permission} is declared but "
                f"there is no <uses-feature android:name=\"{feature}\" "
                f"android:required=\"false\" /> -- Android derives this feature "
                f"as required=\"true\" from the permission alone, which makes "
                f"the app uninstallable on hardware without it.",
                file=sys.stderr,
            )

if fail:
    sys.exit(1)
print(f"guard-android-manifest-features: OK ({manifest_path})")
PY
