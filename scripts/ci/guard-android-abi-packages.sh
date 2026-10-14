#!/usr/bin/env bash
#
# ut-docs#1541: every Android device downloads only the APK built for its own
# ABI. Three files have to agree for that to keep working, and nothing but a
# real release (or a real device failing to update) would notice if one
# drifted:
#   1. android/app/build.gradle.kts declares the arm64 / armv7 / universal
#      product flavors and compresses native libraries
#      (useLegacyPackaging = true; without it each APK doubles in size);
#   2. every stable update URL the app bakes in (unitill-pos-android*-latest.apk)
#      is a name .github/workflows/release.yml actually publishes — an app
#      pointing at a name no release carries can never update again;
#   3. release.yml builds all three flavors.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GRADLE="${1:-android/app/build.gradle.kts}"
RELEASE="${2:-.github/workflows/release.yml}"
[ -f "${GRADLE}" ] || { echo "guard-android-abi-packages: ${GRADLE} missing" >&2; exit 1; }
[ -f "${RELEASE}" ] || { echo "guard-android-abi-packages: ${RELEASE} missing" >&2; exit 1; }

GRADLE="${GRADLE}" RELEASE="${RELEASE}" python3 - <<'PY'
import os
import re
import sys

gradle_path = os.environ["GRADLE"]
release_path = os.environ["RELEASE"]
gradle = open(gradle_path, encoding="utf-8").read()
release = open(release_path, encoding="utf-8").read()
# Comments say things like "the universal flavor"; only code counts.
code = "\n".join(re.sub(r"//.*", "", line) for line in gradle.splitlines())

errors = []
for flavor in ("arm64", "armv7", "universal"):
    if not re.search(r'create\("%s"\)' % flavor, code):
        errors.append(f"{gradle_path} has no `{flavor}` product flavor")
    task = "assemble%sRelease" % flavor.capitalize()
    if task not in release:
        errors.append(f"{release_path} never runs {task}")
if not re.search(r"useLegacyPackaging\s*=\s*true", code):
    errors.append(f"{gradle_path} does not set packaging.jniLibs.useLegacyPackaging = true (native libraries ship uncompressed, ~2x the download)")

names = sorted(set(re.findall(r'"(unitill-pos-android(?:-[a-z0-9]+)?-latest\.apk)"', code)))
if not names:
    errors.append(f"{gradle_path} bakes in no unitill-pos-android*-latest.apk update URL")
for name in names:
    uploaded = re.search(r"gh release upload[^\n]*(?:\\\n[^\n]*)*" + re.escape(name), release)
    if not uploaded:
        errors.append(f"the app updates from {name}, but {release_path} never uploads it to a release")

if errors:
    for e in errors:
        print(f"guard-android-abi-packages: {e}", file=sys.stderr)
    sys.exit(1)
print(f"guard-android-abi-packages: OK ({', '.join(names)})")
PY
