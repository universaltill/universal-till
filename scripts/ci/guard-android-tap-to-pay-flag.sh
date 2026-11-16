#!/usr/bin/env bash
#
# #3048 / ADR-0125 Amendment 1: Stripe Terminal Tap to Pay is a COMPILE-TIME
# flag (-PtapToPay=true, arm64 flavor only), default OFF. A shipped build must
# never carry it by accident, so no workflow may pass it except android-ci.yml,
# which only compiles and runs JVM tests (it never signs, uploads or releases).
# Turning it on for a release is one line in release.yml in a card the owner
# signs; that card adds android/tap-to-pay-release-approved.txt, which is the
# marker this guard accepts.
#
# Usage: guard-android-tap-to-pay-flag.sh [workflows-dir] [approval-marker]
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

DIR="${1:-.github/workflows}"
MARKER="${2:-android/tap-to-pay-release-approved.txt}"
[ -d "${DIR}" ] || { echo "guard-android-tap-to-pay-flag: ${DIR} missing" >&2; exit 1; }

fail=0
shopt -s nullglob
for f in "${DIR}"/*.yml "${DIR}"/*.yaml; do
  name="$(basename "${f}")"
  # Match every spelling Gradle accepts: -PtapToPay=true, -PtapToPay="true", -P tapToPay=true.
  hits="$(grep -nE "tapToPay[\"']?[[:space:]]*=[[:space:]]*[\"']?true" "${f}" || true)"
  [ -n "${hits}" ] || continue
  if [ "${name}" = "android-ci.yml" ]; then
    # Allowed only on compile/test lines: never a release/bundle/upload line.
    bad="$(grep -nE "tapToPay[\"']?[[:space:]]*=[[:space:]]*[\"']?true" "${f}" | grep -iE 'release|bundle|publish|upload|sign' || true)"
    if [ -n "${bad}" ]; then
      echo "guard-android-tap-to-pay-flag: ${f} passes tapToPay=true on a release/publish line:" >&2
      echo "${bad}" >&2
      fail=1
    fi
    continue
  fi
  if [ -f "${MARKER}" ]; then
    continue
  fi
  echo "guard-android-tap-to-pay-flag: ${f} enables tapToPay=true but ${MARKER} does not exist." >&2
  echo "${hits}" >&2
  echo "Tap to Pay ships dark until an owner-signed card adds that marker (ADR-0125 Amendment 1, #3048)." >&2
  fail=1
done

[ "${fail}" -eq 0 ] || exit 1
echo "guard-android-tap-to-pay-flag: OK"
