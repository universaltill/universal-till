#!/usr/bin/env bash
#
# ut-docs#3049: Tap to Pay on iPhone ships default OFF. Apple's
# com.apple.developer.proximity-reader.payment.acceptance grant is
# development-only (ut-docs#3047), so the entitlement may only ever be
# signed into a local Debug device build that a developer asked for with
# UT_TAP_TO_PAY=YES — never CI, never TestFlight/Release. This guard pins
# that in ios/project.yml and around it. It fails when:
#
#   1. the UniversalTill target's base settings don't set UT_TAP_TO_PAY: "NO";
#   2. its Release config doesn't set UT_TAP_TO_PAY: "NO",
#      UT_TAP_TO_PAY_INFO: "NO" and CODE_SIGN_ENTITLEMENTS: "", or the
#      Info.plist switch UTTapToPayEnabled isn't $(UT_TAP_TO_PAY_INFO). A
#      command-line UT_TAP_TO_PAY=YES beats the config's own "NO", so the
#      two things that matter in Release — the entitlement and the switch
#      the app reads — hang off settings a command line doesn't name;
#   3. any file under .github/, ios/ or scripts/, or the Makefile — other
#      than ios/README.md (which documents the local opt-in) and this
#      guard's own files — assigns UT_TAP_TO_PAY or UT_TAP_TO_PAY_INFO
#      anything but NO (or, in project.yml, the $(UT_TAP_TO_PAY)
#      pass-through): =YES, : "YES", or a value from a variable;
#   4. ios/UniversalTill/UniversalTill.dev.entitlements holds any key other
#      than com.apple.developer.proximity-reader.payment.acceptance.
#
# ut-docs#6460 relaxes rule 2 once Apple's distribution grant is recorded.
#
# Usage: guard-ios-tap-to-pay-default-off.sh [root]  (root defaults to the
# repo; guard-ios-tap-to-pay-default-off_test.sh points it at fixtures).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TARGET="${1:-${ROOT_DIR}}"
[ -f "${TARGET}/ios/project.yml" ] || { echo "guard-ios-tap-to-pay-default-off: ${TARGET}/ios/project.yml missing" >&2; exit 1; }

TARGET="${TARGET}" python3 - <<'PY'
import os
import plistlib
import re
import subprocess
import sys

root = os.environ["TARGET"]
fail = False

def err(msg):
    global fail
    fail = True
    print(f"guard-ios-tap-to-pay-default-off: {msg}", file=sys.stderr)

# A minimal indentation parser for project.yml's mapping subset: enough to
# read targets.UniversalTill.settings.{base,configs.Release}. List items and
# comments are skipped; a scalar keeps its raw text (quotes included).
def parse(text):
    tree = {}
    stack = [(-1, tree)]
    for raw in text.splitlines():
        line = raw.split(" #", 1)[0].rstrip() if not raw.lstrip().startswith("#") else ""
        if not line.strip() or line.lstrip().startswith("- "):
            continue
        indent = len(line) - len(line.lstrip())
        m = re.match(r"^\s*([^:]+?)\s*:\s*(.*)$", line)
        if not m:
            continue
        key, val = m.group(1).strip().strip('"'), m.group(2).strip()
        while stack and stack[-1][0] >= indent:
            stack.pop()
        parent = stack[-1][1]
        if val == "":
            node = {}
            parent[key] = node
            stack.append((indent, node))
        else:
            parent[key] = val

    return tree

with open(os.path.join(root, "ios", "project.yml"), encoding="utf-8") as f:
    project = parse(f.read())

def dig(d, *keys):
    for k in keys:
        if not isinstance(d, dict) or k not in d:
            return None
        d = d[k]
    return d

settings = dig(project, "targets", "UniversalTill", "settings") or {}
base = settings.get("base") or {}
release = dig(settings, "configs", "Release") or {}

def unquote(v):
    return v[1:-1] if isinstance(v, str) and len(v) >= 2 and v[0] == v[-1] and v[0] in "\"'" else v

# 1. Base default.
if unquote(base.get("UT_TAP_TO_PAY")) != "NO":
    err('ios/project.yml: targets.UniversalTill.settings.base must set UT_TAP_TO_PAY: "NO" (default OFF, ut-docs#3049).')

# 2. Release never carries the entitlement.
if unquote(release.get("UT_TAP_TO_PAY")) != "NO":
    err('ios/project.yml: the Release config must set UT_TAP_TO_PAY: "NO" (Apple\'s grant is development-only, ut-docs#3047).')
if unquote(release.get("UT_TAP_TO_PAY_INFO")) != "NO":
    err('ios/project.yml: the Release config must set UT_TAP_TO_PAY_INFO: "NO" (a command-line UT_TAP_TO_PAY=YES '
        'must not switch a Release build on).')
enabled = dig(project, "targets", "UniversalTill", "info", "properties", "UTTapToPayEnabled")
if unquote(enabled) != "$(UT_TAP_TO_PAY_INFO)":
    err('ios/project.yml: info.properties.UTTapToPayEnabled must be $(UT_TAP_TO_PAY_INFO), the switch Release pins to NO.')
if "CODE_SIGN_ENTITLEMENTS" not in release or unquote(release.get("CODE_SIGN_ENTITLEMENTS")) != "":
    err('ios/project.yml: the Release config must set CODE_SIGN_ENTITLEMENTS: "" (no Tap to Pay entitlement in TestFlight).')

# 3. Nothing a build can run turns the flag on (README documents it). Any
#    assigned value other than NO counts — YES, or one read from a variable
#    — except project.yml's own $(UT_TAP_TO_PAY) pass-through.
turn_on = re.compile(r"""\bUT_TAP_TO_PAY(?:_INFO)?\s*[:=]\s*["']?(?!NO\b|\$\(UT_TAP_TO_PAY\))[^\s"']+""")
TOPS = (".github", "ios", "scripts", "Makefile")
SKIP = {"ios/README.md", "scripts/ci/guard-ios-tap-to-pay-default-off.sh",
        "scripts/ci/guard-ios-tap-to-pay-default-off_test.sh"}
def candidate_files():
    try:
        out = subprocess.run(["git", "-C", root, "ls-files", "-co", "--exclude-standard", "--", *TOPS],
                             check=True, capture_output=True, text=True).stdout
        if subprocess.run(["git", "-C", root, "rev-parse", "--show-toplevel"], capture_output=True,
                          text=True).stdout.strip() == os.path.realpath(root):
            return [l for l in out.splitlines() if l]
    except (subprocess.CalledProcessError, FileNotFoundError):
        pass
    files = []
    for top in TOPS:
        if os.path.isfile(os.path.join(root, top)):
            files.append(top)
            continue
        for d, _, names in os.walk(os.path.join(root, top)):
            if "/build" in d.replace(root, "") or ".xcodeproj" in d:
                continue
            files += [os.path.relpath(os.path.join(d, n), root) for n in names]
    return files

for rel in sorted(candidate_files()):
    if rel in SKIP:
        continue
    path = os.path.join(root, rel)
    try:
        with open(path, encoding="utf-8") as f:
            text = f.read()
    except (UnicodeDecodeError, IsADirectoryError, FileNotFoundError):
        continue
    for m in turn_on.finditer(text):
        line = text.count("\n", 0, m.start()) + 1
        err(f"{rel}:{line} turns Tap to Pay on ({m.group(0)}); only a local dev-signed device build may (ios/README.md).")

# 4. The dev entitlements file holds the one Tap to Pay key only.
ent = os.path.join(root, "ios", "UniversalTill", "UniversalTill.dev.entitlements")
if os.path.exists(ent):
    try:
        with open(ent, "rb") as f:
            keys = set(plistlib.load(f).keys())
    except Exception as e:  # noqa: BLE001 — any parse failure is a failure
        err(f"ios/UniversalTill/UniversalTill.dev.entitlements is not a valid plist: {e}")
        keys = set()
    extra = keys - {"com.apple.developer.proximity-reader.payment.acceptance"}
    for k in sorted(extra):
        err(f"ios/UniversalTill/UniversalTill.dev.entitlements holds {k}; only the Tap to Pay key belongs there.")

if fail:
    sys.exit(1)
print("guard-ios-tap-to-pay-default-off: OK (Tap to Pay default OFF; Release never carries the entitlement)")
PY
