#!/usr/bin/env bash
#
# Regression test for guard-android-tap-to-pay-flag.sh (#3048): shows the guard
# rejects a release workflow that turns the flag on, accepts android-ci's
# compile-only use, and accepts the owner-approved marker. Fixtures only.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
GUARD="scripts/ci/guard-android-tap-to-pay-flag.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAIL=0

mk() { rm -rf "${TMP}/wf" "${TMP}/marker"; mkdir -p "${TMP}/wf"; }
expect() { # label want(0|1) [marker]
  local label="$1" want="$2" marker="${3:-${TMP}/no-such-marker}" rc=0
  bash "${GUARD}" "${TMP}/wf" "${marker}" >/dev/null 2>&1 || rc=$?
  if { [ "${want}" = 0 ] && [ "${rc}" = 0 ]; } || { [ "${want}" = 1 ] && [ "${rc}" != 0 ]; }; then
    echo "PASS: ${label}"
  else
    echo "FAIL: ${label} (exit ${rc})"; FAIL=$((FAIL + 1))
  fi
}

mk; echo './gradlew assembleArm64Release' > "${TMP}/wf/release.yml"
expect "release without the flag is fine" 0

mk; echo './gradlew -PtapToPay=true assembleArm64Release' > "${TMP}/wf/release.yml"
expect "release with the flag is rejected" 1

mk; echo './gradlew -PtapToPay="true" assembleArm64Release' > "${TMP}/wf/release.yml"
expect "quoted spelling is rejected" 1

mk; echo './gradlew -PtapToPay=true assembleArm64Release' > "${TMP}/wf/release.yml"; : > "${TMP}/marker"
expect "owner-approved marker allows it" 0 "${TMP}/marker"

mk; echo './gradlew -PtapToPay=true assembleArm64Debug testArm64DebugUnitTest' > "${TMP}/wf/android-ci.yml"
expect "android-ci compile/test use is allowed" 0

mk; echo './gradlew -PtapToPay=true assembleArm64Release' > "${TMP}/wf/android-ci.yml"
expect "android-ci must not build a release with it" 1

mk; echo './gradlew -PtapToPay=false assembleArm64Debug' > "${TMP}/wf/other.yml"
expect "explicit false is fine" 0

mk; echo 'run: ./gradlew -PtapToPay=true testArm64DebugUnitTest' > "${TMP}/wf/ci.yml"
expect "any other workflow is rejected" 1

[ "${FAIL}" -eq 0 ] || { echo "${FAIL} case(s) failed" >&2; exit 1; }
echo "guard-android-tap-to-pay-flag_test: all cases passed"
