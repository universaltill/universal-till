#!/usr/bin/env bash
#
# Regression test for guard-android-tap-to-pay-flag.sh (#3048): the guard rejects every
# way of turning the flag on for a shipped build and accepts android-ci's compile-only
# use. Fixture trees only.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
GUARD="scripts/ci/guard-android-tap-to-pay-flag.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAIL=0
R="${TMP}/repo"

mk() { rm -rf "${R}" "${TMP}/marker"; mkdir -p "${R}/.github/workflows" "${R}/.github/actions/x" "${R}/scripts" "${R}/android"; }
put() { mkdir -p "$(dirname "${R}/$1")"; cat > "${R}/$1"; }
expect() { # label want(0|1) [marker]
  local label="$1" want="$2" marker="${3:-${TMP}/no-such-marker}" rc=0
  bash "${GUARD}" "${R}" "${marker}" >/dev/null 2>&1 || rc=$?
  if { [ "${want}" = 0 ] && [ "${rc}" = 0 ]; } || { [ "${want}" = 1 ] && [ "${rc}" != 0 ]; }; then
    echo "PASS: ${label}"
  else
    echo "FAIL: ${label} (exit ${rc})"; FAIL=$((FAIL + 1))
  fi
}

mk; echo './gradlew assembleArm64Release' | put .github/workflows/release.yml
expect "release without the flag is fine" 0

mk; echo './gradlew -PtapToPay=true assembleArm64Release' | put .github/workflows/release.yml
expect "release with the flag is rejected" 1

mk; echo './gradlew -PtapToPay="true" assembleArm64Release' | put .github/workflows/release.yml
expect "quoted spelling is rejected" 1

mk; echo './gradlew -PtapToPay=1 assembleArm64Release' | put .github/workflows/release.yml
expect "any value but false is rejected (1)" 1

mk; echo "./gradlew -PtapToPay=\$FLAG assembleArm64Release" | put .github/workflows/release.yml
expect "an indirect value is rejected" 1

mk; echo './gradlew -PtapToPay=false assembleArm64Release' | put .github/workflows/release.yml
expect "explicit false is fine" 0

mk; printf 'env:\n  ORG_GRADLE_PROJECT_tapToPay: "true"\n' | put .github/workflows/release.yml
expect "ORG_GRADLE_PROJECT env is rejected" 1

mk; echo 'tapToPay=true' | put android/gradle.properties
expect "gradle.properties is rejected" 1

mk; echo 'tapToPay=false' | put android/gradle.properties
expect "gradle.properties false is fine" 0

mk; echo './gradlew -PtapToPay=true assembleArm64Release' | put scripts/release.sh
expect "a script is rejected" 1

mk; echo 'run: ./gradlew -PtapToPay=true assembleArm64Release' | put .github/actions/x/action.yml
expect "a composite action is rejected" 1

mk; echo 'run: ./gradlew -PtapToPay=true testArm64DebugUnitTest' | put .github/workflows/ci.yml
expect "any other workflow is rejected" 1

# The iOS switch name is spelled in pieces so this file itself never reads as
# turning iOS Tap to Pay on (guard-ios-tap-to-pay-default-off.sh scans scripts/).
IOS_SW="UT_TAP_""TO_PAY"
mk; printf '%s: "Y""ES"\n%s=Y""ES\nenv %s="N""O"\n' "${IOS_SW}" "${IOS_SW}" "${IOS_SW}" | put scripts/ci/guard-ios-tap-to-pay-default-off.sh
expect "the iOS UT_TAP_TO_PAY switch is not the Android flag" 0

mk; echo 'ORG_GRADLE_PROJECT_tapToPay=true' | put scripts/x.sh
expect "ORG_GRADLE_PROJECT_tapToPay is still rejected" 1

mk; echo 'tapToPay=true' | put android/gradle.properties
expect "gradle.properties tapToPay=true is still rejected" 1

mk; echo '# note: -PtapToPay=true is never passed here' | put .github/workflows/release.yml
expect "a comment is not an assignment" 0

mk; echo './gradlew -PtapToPay=true assembleArm64Release' | put .github/workflows/release.yml; : > "${TMP}/marker"
expect "owner-approved marker allows it" 0 "${TMP}/marker"

mk; printf '      - name: compile\n        run: ./gradlew -PtapToPay=true assembleArm64Debug testArm64DebugUnitTest\n' | put .github/workflows/android-ci.yml
expect "android-ci compile/test use is allowed" 0

mk; printf '      - name: compile\n        run: ./gradlew -PtapToPay=true assembleArm64Release\n' | put .github/workflows/android-ci.yml
expect "android-ci single-line release is rejected" 1

mk; printf '      - name: build\n        run: |\n          ./gradlew -PtapToPay=true \\\n            assembleArm64Release\n' | put .github/workflows/android-ci.yml
expect "android-ci multi-line release is rejected" 1

mk; printf '      - name: build\n        run: |\n          ./gradlew -PtapToPay=true assembleArm64Debug\n          gh release upload v1 app.apk\n' | put .github/workflows/android-ci.yml
expect "android-ci upload in the same step is rejected" 1

mk; printf '      - name: build\n        run: ./gradlew -PtapToPay=true assembleArm64Debug\n      - name: release notes\n        run: echo hi\n' | put .github/workflows/android-ci.yml
expect "release wording in ANOTHER step is fine" 0

[ "${FAIL}" -eq 0 ] || { echo "${FAIL} case(s) failed" >&2; exit 1; }
echo "guard-android-tap-to-pay-flag_test: all cases passed"
