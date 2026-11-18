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
mkaab() { # name content: an AAB keeps its dex under base/dex/
  ( cd "${TMP}" && mkdir -p "a_$1/base/dex" && printf '%s' "$2" > "a_$1/base/dex/classes.dex" && rm -f "$1.aab" && (cd "a_$1" && zip -q -r "../$1.aab" base) )
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
mkaab aabclean 'Lcom/universaltill/pos/MainActivity;'
mkaab aabstripe 'Lcom/stripe/stripeterminal/Terminal;'
expect "clean aab passes absent" 0 absent "${TMP}/aabclean.aab"
expect "sdk aab fails absent" 1 absent "${TMP}/aabstripe.aab"
expect "sdk aab passes present" 0 present "${TMP}/aabstripe.aab"
printf 'not a zip' > "${TMP}/junk.apk"
expect "unreadable file fails absent" 1 absent "${TMP}/junk.apk"
( cd "${TMP}" && mkdir -p z_nodex && printf 'x' > z_nodex/readme.txt && rm -f nodex.apk && cd z_nodex && zip -q ../nodex.apk readme.txt )
expect "zip with no dex fails absent" 1 absent "${TMP}/nodex.apk"
# A CRC-damaged dex: flip one payload byte inside the stored (uncompressed) entry.
( cd "${TMP}" && mkdir -p z_crc && printf '%s' 'Lcom/universaltill/pos/MainActivity; padding padding' > z_crc/classes.dex && rm -f crc.apk && (cd z_crc && zip -q -0 ../crc.apk classes.dex) )
LC_ALL=C sed -i.bak 's/padding padding/padding paddinX/' "${TMP}/crc.apk"
expect "crc-damaged dex fails absent" 1 absent "${TMP}/crc.apk"
expect "bad mode is a usage error" 1 maybe "${TMP}/clean.apk"

[ "${FAIL}" -eq 0 ] || { echo "${FAIL} case(s) failed" >&2; exit 1; }
echo "check-android-stripe-sdk_test: all cases passed"
