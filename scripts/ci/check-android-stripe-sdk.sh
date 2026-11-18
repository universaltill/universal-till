#!/usr/bin/env bash
#
# #3048: proves a built APK does / does not carry the Stripe Terminal SDK.
# Shipped flavors (armv7, universal, arm64 without -PtapToPay) must have none
# of it in any classes*.dex; the -PtapToPay=true arm64 build must have it
# (otherwise the flag silently did nothing and CI proved nothing).
#
# Usage: check-android-stripe-sdk.sh absent|present <apk>...
set -euo pipefail

MODE="${1:-}"
case "${MODE}" in absent | present) ;; *) echo "usage: $0 absent|present <apk>..." >&2; exit 2 ;; esac
shift
[ "$#" -gt 0 ] || { echo "usage: $0 absent|present <apk>..." >&2; exit 2; }

fail=0
for apk in "$@"; do
  [ -f "${apk}" ] || { echo "check-android-stripe-sdk: ${apk} missing" >&2; fail=1; continue; }
  n=0
  # unzip -p exits 11 when the pattern matches nothing; treat as zero dex files.
  if dex="$(unzip -p "${apk}" 'classes*.dex' 2>/dev/null | grep -a -c 'com/stripe/stripeterminal' || true)"; then n="${dex:-0}"; fi
  if [ "${MODE}" = absent ] && [ "${n}" != 0 ]; then
    echo "check-android-stripe-sdk: ${apk} contains the Stripe Terminal SDK but must not" >&2
    fail=1
  elif [ "${MODE}" = present ] && [ "${n}" = 0 ]; then
    echo "check-android-stripe-sdk: ${apk} lacks the Stripe Terminal SDK although tapToPay=true" >&2
    fail=1
  else
    echo "check-android-stripe-sdk: ${MODE} OK (${apk})"
  fi
done
[ "${fail}" -eq 0 ]
