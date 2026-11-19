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
val defaultUpdateApkUrlBase = "https://github.com/universaltill/universal-till/releases/latest/download/"
val updateApkUrlBase = (project.findProperty("updateApkUrlBase") as String?) ?: defaultUpdateApkUrlBase
require(isValidUpdateApkUrlBase(updateApkUrlBase)) { "updateApkUrlBase must be valid" }
val a = updateApkUrlBase + "unitill-pos-android-arm64-latest.apk"
val b = updateApkUrlBase + "unitill-pos-android-armv7-latest.apk"
val c = updateApkUrlBase + "unitill-pos-android-latest.apk"
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
      - run: ./gradlew assembleArm64Release assembleArmv7Release assembleUniversalRelease -PversionName=1 -PupdateApkUrlBase="$B"
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

# 7. ut-docs#5271: our default base literal changed (existing tills / our APKs move).
good_gradle; good_release
sed -i.bak 's#universaltill/universal-till/releases#someone/else/releases#' "${GRADLE}"
assert_fails "default update base literal changed"

# 8. The property is no longer read (a fork could not set it).
good_gradle; good_release
sed -i.bak 's/findProperty("updateApkUrlBase")/findProperty("other")/' "${GRADLE}"
assert_fails "updateApkUrlBase property not read"

# 9. The base is not validated.
good_gradle; good_release
sed -i.bak '/^require(/d' "${GRADLE}"
assert_fails "updateApkUrlBase not validated by require()"

# 10. A second hard-coded https URL (an UPDATE_APK_URL that bypasses the base).
good_gradle; good_release
echo 'val sneaky = "https://evil.example/unitill-pos-android-latest.apk"' >> "${GRADLE}"
assert_fails "extra https literal in the Gradle code"

# 11. release.yml builds without passing the property.
good_gradle; good_release
sed -i.bak 's/ -PupdateApkUrlBase=[^ ]*//' "${RELEASE}"
assert_fails "release.yml does not pass -PupdateApkUrlBase"

# 12. A URL built from the default base bypasses the property.
good_gradle; good_release
echo 'val bypass = defaultUpdateApkUrlBase + "unitill-pos-android-arm64-latest.apk"' >> "${GRADLE}"
assert_fails "a URL derived from defaultUpdateApkUrlBase, not the property"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "guard-android-abi-packages_test: ${FAIL_COUNT} failure(s)"
  exit 1
fi
echo "guard-android-abi-packages_test: all cases passed"
