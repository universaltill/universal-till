#!/usr/bin/env bash
#
# Regression test for guard-ios-usage-descriptions.sh (ut-docs#3354): proves
# the guard rejects the shape of the original bug -- a web getUserMedia
# audio call with no NSMicrophoneUsageDescription in ios/project.yml -- and
# an untranslated usage description, rather than merely passing on the
# fixed tree. Runs against a throwaway fixture tree, never tracked source.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-ios-usage-descriptions.sh"
FIX="$(mktemp -d -t guard-ios-usage-XXXXXX)"
OUT="$(mktemp -t guard-ios-usage-out-XXXXXX)"
cleanup() { rm -rf "${FIX}"; rm -f "${OUT}"; }
trap cleanup EXIT

FAIL_COUNT=0

# A fresh fixture: project.yml with the given usage keys, en+tr .lproj
# translating every one of them, and one web file with the given JS.
fixture() {
  local keys="$1" js="$2" key
  rm -rf "${FIX:?}/ios" "${FIX:?}/web"
  mkdir -p "${FIX}/ios/UniversalTill/en.lproj" "${FIX}/ios/UniversalTill/tr.lproj" "${FIX}/web/ui/partials" "${FIX}/web/public/vendor"
  {
    echo "targets:"
    echo "  UniversalTill:"
    echo "    info:"
    echo "      properties:"
    for key in ${keys}; do echo "        ${key}: Some reason."; done
  } > "${FIX}/ios/project.yml"
  for lang in en tr; do
    : > "${FIX}/ios/UniversalTill/${lang}.lproj/InfoPlist.strings"
    for key in ${keys}; do
      echo "\"${key}\" = \"Some reason.\";" >> "${FIX}/ios/UniversalTill/${lang}.lproj/InfoPlist.strings"
    done
  done
  printf '%s\n' "${js}" > "${FIX}/web/ui/partials/panel.html"
}

assert_passes() {
  if bash "${GUARD}" "${FIX}" >"${OUT}" 2>&1; then
    echo "PASS (correctly accepted): $1"
  else
    echo "FAIL (should have been accepted but was rejected): $1"; cat "${OUT}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

assert_fails() {
  if bash "${GUARD}" "${FIX}" >"${OUT}" 2>&1; then
    echo "FAIL (should have been rejected but was accepted): $1"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "PASS (correctly rejected): $1"
  fi
}

CAM=NSCameraUsageDescription
MIC=NSMicrophoneUsageDescription

# 1. The original bug: an audio capture with only the camera key declared.
fixture "${CAM}" "navigator.mediaDevices.getUserMedia({ audio: true })"
assert_fails "audio getUserMedia without ${MIC}"

# 2. The fix.
fixture "${CAM} ${MIC}" "navigator.mediaDevices.getUserMedia({ audio: true })"
assert_passes "audio getUserMedia with ${MIC}"

# 3. A video capture (nested constraint object) needs the camera key.
fixture "${MIC}" "navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } })"
assert_fails "video getUserMedia without ${CAM}"
fixture "${CAM}" "navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } })"
assert_passes "video getUserMedia with ${CAM}"

# 4. Constraints passed as a variable: the guard can't tell, so both keys.
fixture "${CAM}" "navigator.mediaDevices.getUserMedia(constraints)"
assert_fails "non-literal getUserMedia constraints with only ${CAM}"
fixture "${CAM} ${MIC}" "navigator.mediaDevices.getUserMedia(constraints)"
assert_passes "non-literal getUserMedia constraints with both keys"

# 5. Vendored code is not ours to wire up.
fixture "" "x"
printf '%s\n' "navigator.mediaDevices.getUserMedia({ audio: true })" > "${FIX}/web/public/vendor/lib.js"
assert_passes "getUserMedia only under web/public/vendor/"

# 6. A declared key with no translation in one .lproj.
fixture "${CAM} ${MIC}" "x"
grep -v "${MIC}" "${FIX}/ios/UniversalTill/tr.lproj/InfoPlist.strings" > "${OUT}"
cp "${OUT}" "${FIX}/ios/UniversalTill/tr.lproj/InfoPlist.strings"
assert_fails "${MIC} missing from tr.lproj/InfoPlist.strings"

# 7. The real tree must pass -- the guard is wired to today's fix.
if bash "${GUARD}" >"${OUT}" 2>&1; then
  echo "PASS (correctly accepted): real repository tree"
else
  echo "FAIL: real repository tree was rejected"; cat "${OUT}"
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "guard-ios-usage-descriptions_test: ${FAIL_COUNT} assertion(s) failed" >&2
  exit 1
fi
echo "guard-ios-usage-descriptions_test: all assertions passed"
