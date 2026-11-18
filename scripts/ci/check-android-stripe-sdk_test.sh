#!/usr/bin/env bash
# Regression test for check-android-stripe-sdk.sh (#3048). Builds throwaway zips
# that look like APKs; never touches a real build.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
CHECK="scripts/ci/check-android-stripe-sdk.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAIL=0

mkzip() { # name content
  ( cd "${TMP}" && mkdir -p "z_$1" && printf '%s' "$2" > "z_$1/classes.dex" && rm -f "$1.apk" && (cd "z_$1" && zip -q "../$1.apk" classes.dex) )
}
expect() { # label want(0|1) args...
  local label="$1" want="$2" rc=0
  shift 2
  bash "${CHECK}" "$@" >/dev/null 2>&1 || rc=$?
  if { [ "${want}" = 0 ] && [ "${rc}" = 0 ]; } || { [ "${want}" = 1 ] && [ "${rc}" != 0 ]; }; then
    echo "PASS: ${label}"
  else
    echo "FAIL: ${label} (exit ${rc})"; FAIL=$((FAIL + 1))
  fi
}

mkzip clean 'Lcom/universaltill/pos/MainActivity;'
mkzip stripe 'Lcom/universaltill/pos/MainActivity; Lcom/stripe/stripeterminal/Terminal;'
expect "clean apk passes absent" 0 absent "${TMP}/clean.apk"
expect "sdk apk fails absent" 1 absent "${TMP}/stripe.apk"
expect "sdk apk passes present" 0 present "${TMP}/stripe.apk"
expect "clean apk fails present" 1 present "${TMP}/clean.apk"
expect "one bad apk among several fails" 1 absent "${TMP}/clean.apk" "${TMP}/stripe.apk"
expect "missing apk fails" 1 absent "${TMP}/nope.apk"
expect "bad mode is a usage error" 1 maybe "${TMP}/clean.apk"

[ "${FAIL}" -eq 0 ] || { echo "${FAIL} case(s) failed" >&2; exit 1; }
echo "check-android-stripe-sdk_test: all cases passed"
