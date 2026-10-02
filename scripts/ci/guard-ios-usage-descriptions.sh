#!/usr/bin/env bash
#
# ut-docs#3354: the bug-report panel's voice recorder calls
# getUserMedia({ audio: true }), but the iOS shell's Info.plist
# (ios/project.yml) declared only NSCameraUsageDescription. iOS kills an
# app that touches a privacy-protected resource without the matching
# usage-description string, so pressing Record crashed the iPhone app.
# Nothing on Linux CI or the simulator build notices: the plist is valid
# either way and the crash only happens on a device, on the first tap.
#
# This guard ties the web UI's capture calls to the iOS plist:
#   1. every getUserMedia(...) call under web/ (vendor/ excluded) that asks
#      for audio needs NSMicrophoneUsageDescription, and one that asks for
#      video needs NSCameraUsageDescription. A call whose constraints aren't
#      an inline object literal needs both — the guard can't tell.
#   2. every NS*UsageDescription declared in ios/project.yml has a
#      translation in every ios/UniversalTill/<lang>.lproj/InfoPlist.strings,
#      so the permission prompt never falls back to English silently.
#
# Usage: guard-ios-usage-descriptions.sh [root]   (root defaults to the repo;
# the regression test points it at throwaway fixtures).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TARGET="${1:-${ROOT_DIR}}"
[ -f "${TARGET}/ios/project.yml" ] || { echo "guard-ios-usage-descriptions: ${TARGET}/ios/project.yml missing" >&2; exit 1; }

TARGET="${TARGET}" python3 - <<'PY'
import glob
import os
import re
import sys

root = os.environ["TARGET"]
project = os.path.join(root, "ios", "project.yml")

with open(project, encoding="utf-8") as f:
    declared = set(re.findall(r"^\s+(NS\w+UsageDescription)\s*:\s*\S", f.read(), re.M))

fail = False

def err(msg):
    global fail
    fail = True
    print(f"guard-ios-usage-descriptions: {msg}", file=sys.stderr)

# 1. Web capture calls -> required plist keys.
call = re.compile(r"getUserMedia\s*\(\s*([^)]*)")
for path in sorted(glob.glob(os.path.join(root, "web", "**", "*"), recursive=True)):
    rel = os.path.relpath(path, root)
    if not path.endswith((".html", ".js")) or "/vendor/" in path or not os.path.isfile(path):
        continue
    with open(path, encoding="utf-8") as f:
        text = f.read()
    for m in call.finditer(text):
        args = m.group(1).strip()
        line = text.count("\n", 0, m.start()) + 1
        if args.startswith("{"):
            needs = set()
            if re.search(r"\baudio\s*:", args):
                needs.add("NSMicrophoneUsageDescription")
            if re.search(r"\bvideo\s*:", args):
                needs.add("NSCameraUsageDescription")
        else:
            needs = {"NSMicrophoneUsageDescription", "NSCameraUsageDescription"}
        for key in sorted(needs - declared):
            err(f"{rel}:{line} calls getUserMedia but ios/project.yml declares no {key} "
                f"-- iOS terminates the app on that call (ut-docs#3354).")

# 2. Every declared usage description is translated in every .lproj.
lprojs = sorted(glob.glob(os.path.join(root, "ios", "UniversalTill", "*.lproj")))
if not lprojs:
    err("no ios/UniversalTill/*.lproj directories found")
for d in lprojs:
    strings = os.path.join(d, "InfoPlist.strings")
    rel = os.path.relpath(strings, root)
    have = set()
    if os.path.isfile(strings):
        with open(strings, encoding="utf-8") as f:
            have = set(re.findall(r'^\s*"(\w+)"\s*=\s*"[^"]+"\s*;', f.read(), re.M))
    for key in sorted(declared - have):
        err(f"{rel} has no translation for {key} declared in ios/project.yml.")

if fail:
    sys.exit(1)
print(f"guard-ios-usage-descriptions: OK ({len(declared)} usage descriptions, {len(lprojs)} locales)")
PY
