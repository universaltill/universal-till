#!/usr/bin/env bash
#
# Regression test for check-android-sideload-install-permission.sh
# (ut-docs#6935). Fixtures live only in a mktemp dir.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHECK="${ROOT_DIR}/scripts/ci/check-android-sideload-install-permission.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

PERM='<uses-permission android:name="android.permission.REQUEST_INSTALL_PACKAGES"/>'
printf '<manifest>\n  %s\n</manifest>\n' "${PERM}" >"${WORK}/with.xml"
printf '<manifest>\n  <uses-permission android:name="android.permission.INTERNET"/>\n</manifest>\n' >"${WORK}/without.xml"
printf '<manifest>\n  <!-- %s -->\n</manifest>\n' "${PERM}" >"${WORK}/commented.xml"
printf '<manifest>\n  <!--\n  %s\n  -->\n</manifest>\n' "${PERM}" >"${WORK}/commented-multiline.xml"

FAIL_COUNT=0
expect() { # <label> <want> <args...>
  local label="$1" want="$2" got=0
  shift 2
  bash "${CHECK}" "$@" >/dev/null 2>&1 || got=$?
  if [ "${got}" = "${want}" ]; then echo "PASS: ${label}"; else echo "FAIL: ${label}: exit ${got}, want ${want}"; FAIL_COUNT=$((FAIL_COUNT + 1)); fi
}

expect "manifest with the permission passes" 0 "${WORK}/with.xml"
expect "manifest without it fails" 1 "${WORK}/without.xml"
expect "commented-out permission fails" 1 "${WORK}/commented.xml"
expect "multi-line commented-out permission fails" 1 "${WORK}/commented-multiline.xml"
expect "one bad manifest among good ones fails" 1 "${WORK}/with.xml" "${WORK}/without.xml"
expect "missing file is a usage error" 2 "${WORK}/nope.xml"
expect "no arguments is a usage error" 2

if [ "${FAIL_COUNT}" -gt 0 ]; then echo "check-android-sideload-install-permission_test: ${FAIL_COUNT} failed"; exit 1; fi
echo "check-android-sideload-install-permission_test: all cases passed"
