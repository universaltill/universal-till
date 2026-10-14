#!/usr/bin/env bash
#
# Regression test for guard-android-abi-packages.sh (ut-docs#1541): proves the
# guard rejects each way the per-ABI packages can drift — a missing flavor, a
# flavor release.yml never builds, uncompressed native libraries, and an
# update URL no release publishes — rather than merely passing on the real
# tree. Runs against throwaway fixtures, never tracked source.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-android-abi-packages.sh"
TMP="$(mktemp -d -t guard-android-abi-packages-XXXXXX)"
GRADLE="${TMP}/build.gradle.kts"
RELEASE="${TMP}/release.yml"
OUT="${TMP}/out"
cleanup() { rm -rf "${TMP}"; }
trap cleanup EXIT

FAIL_COUNT=0

assert_passes() {
  if bash "${GUARD}" "${GRADLE}" "${RELEASE}" >"${OUT}" 2>&1; then
    echo "PASS (correctly accepted): $1"
  else
    echo "FAIL (should have been accepted but was rejected): $1"
    cat "${OUT}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

assert_fails() {
  if bash "${GUARD}" "${GRADLE}" "${RELEASE}" >"${OUT}" 2>&1; then
    echo "FAIL (should have been rejected but was accepted): $1"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "PASS (correctly rejected): $1"
  fi
}

good_gradle() {
  cat > "${GRADLE}" <<'KTS'
val base = "https://example.test/"
val a = base + "unitill-pos-android-arm64-latest.apk"
val b = base + "unitill-pos-android-armv7-latest.apk"
val c = base + "unitill-pos-android-latest.apk"
android {
    productFlavors {
        create("arm64") { }
        create("armv7") { }
        create("universal") { }
    }
    packaging { jniLibs { useLegacyPackaging = true } }
}
KTS
}

good_release() {
  cat > "${RELEASE}" <<'YML'
      - run: ./gradlew assembleArm64Release assembleArmv7Release assembleUniversalRelease
      - run: |
          gh release upload "${TAG}" unitill-pos-android-latest.apk \
            unitill-pos-android-arm64-latest.apk unitill-pos-android-armv7-latest.apk --clobber
YML
}

# 1. The fixed shape passes, and so does the real tree.
good_gradle; good_release
assert_passes "all three flavors, compressed libs, every URL published"
if bash "${GUARD}" >"${OUT}" 2>&1; then
  echo "PASS (correctly accepted): the real build.gradle.kts + release.yml"
else
  echo "FAIL (should have been accepted but was rejected): the real tree"
  cat "${OUT}"
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# 2. A flavor removed.
good_gradle; good_release
sed -i.bak '/create("armv7")/d' "${GRADLE}"
assert_fails "armv7 flavor missing"

# 3. A flavor only mentioned in a comment does not count.
good_gradle; good_release
sed -i.bak 's/create("arm64") { }/\/\/ create("arm64") { }/' "${GRADLE}"
assert_fails "arm64 flavor commented out"

# 4. release.yml stops building one flavor.
good_gradle; good_release
sed -i.bak 's/ assembleArmv7Release//' "${RELEASE}"
assert_fails "release.yml never builds armv7"

# 5. Native libraries back to stored (the 150 MB APK).
good_gradle; good_release
sed -i.bak 's/useLegacyPackaging = true/useLegacyPackaging = false/' "${GRADLE}"
assert_fails "useLegacyPackaging off"

# 6. An update URL no release publishes (the app could never update again).
good_gradle; good_release
sed -i.bak 's/unitill-pos-android-arm64-latest.apk unitill-pos-android-armv7-latest.apk/unitill-pos-android-armv7-latest.apk/' "${RELEASE}"
assert_fails "arm64 stable name never uploaded"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "guard-android-abi-packages_test: ${FAIL_COUNT} failure(s)"
  exit 1
fi
echo "guard-android-abi-packages_test: all cases passed"
