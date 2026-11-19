#!/usr/bin/env bash
#
# Regression test for guard-android-manifest-features.sh (ut-docs#1770):
# proves the guard actually rejects the shape of the original bug -- a
# declared permission with implied Android features and no matching
# <uses-feature required="false"> -- rather than merely passing on the
# fixed manifest. A guard that has never been shown to fail is
# indistinguishable from one whose parsing silently stopped matching.
#
# Runs entirely against a throwaway fixture file (never the real manifest),
# same reasoning as guard-webkit-version_test.sh but without needing a
# backup/restore dance since nothing here touches tracked source.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-android-manifest-features.sh"
FIXTURE="$(mktemp -t guard-android-manifest-features-XXXXXX.xml)"
cleanup() { rm -f "${FIXTURE}"; }
trap cleanup EXIT

FAIL_COUNT=0

write_fixture() {
  cat > "${FIXTURE}"
}

assert_passes() {
  local label="$1"
  if bash "${GUARD}" "${FIXTURE}" >/tmp/guard-out.$$ 2>&1; then
    echo "PASS (correctly accepted): ${label}"
  else
    echo "FAIL (should have been accepted but was rejected): ${label}"
    cat /tmp/guard-out.$$
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard-out.$$
}

assert_fails() {
  local label="$1"
  if bash "${GUARD}" "${FIXTURE}" >/tmp/guard-out.$$ 2>&1; then
    echo "FAIL (should have been rejected but was accepted): ${label}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "PASS (correctly rejected): ${label}"
  fi
  rm -f /tmp/guard-out.$$
}

# 1. The original bug: CAMERA declared, no <uses-feature> at all.
write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-permission android:name="android.permission.CAMERA" />
</manifest>
XML
assert_fails "CAMERA with no matching uses-feature element at all"

# 2. Element present but required defaults to "true" (attribute omitted) --
#    this is the subtler variant: someone adds the element but forgets the
#    attribute, which the Android schema defaults to required="true".
write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-permission android:name="android.permission.CAMERA" />
    <uses-feature android:name="android.hardware.camera" />
</manifest>
XML
assert_fails "CAMERA with uses-feature present but required attribute omitted"

# 3. Element present with required="true" explicitly -- still wrong intent.
write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-permission android:name="android.permission.CAMERA" />
    <uses-feature android:name="android.hardware.camera" android:required="true" />
</manifest>
XML
assert_fails "CAMERA with uses-feature required=\"true\""

# 4. The fix: required="false" explicitly declared.
write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-feature android:name="android.hardware.camera" android:required="false" />
    <uses-permission android:name="android.permission.CAMERA" />
</manifest>
XML
assert_passes "CAMERA with uses-feature required=\"false\""

# 5. A permission with two implied features (ACCESS_FINE_LOCATION) needs
#    BOTH covered, not just one -- catches a partial fix.
write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-feature android:name="android.hardware.location" android:required="false" />
    <uses-permission android:name="android.permission.ACCESS_FINE_LOCATION" />
</manifest>
XML
assert_fails "ACCESS_FINE_LOCATION with only one of its two implied features covered"

write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-feature android:name="android.hardware.location" android:required="false" />
    <uses-feature android:name="android.hardware.location.gps" android:required="false" />
    <uses-permission android:name="android.permission.ACCESS_FINE_LOCATION" />
</manifest>
XML
assert_passes "ACCESS_FINE_LOCATION with both implied features covered"

# 6. A permission with no implied features at all (e.g. INTERNET) needs no
#    uses-feature element -- the guard must not false-positive on it.
write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-permission android:name="android.permission.INTERNET" />
</manifest>
XML
assert_passes "INTERNET (no implied feature) with no uses-feature element"

# 6b. #3048: NFC and READ_PHONE_STATE (Stripe Terminal's manifests) imply
#     nfc/telephony features, so they too need required="false".
write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-permission android:name="android.permission.NFC" />
</manifest>
XML
assert_fails "NFC with no matching uses-feature"

write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-feature android:name="android.hardware.nfc" android:required="false" />
    <uses-permission android:name="android.permission.NFC" />
</manifest>
XML
assert_passes "NFC with uses-feature required=false"

write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-permission android:name="android.permission.READ_PHONE_STATE" />
</manifest>
XML
assert_fails "READ_PHONE_STATE with no telephony uses-feature"

# 6c. #3048: the "shipped" profile (merged manifest of armv7, universal and
#     arm64 without -PtapToPay) must carry no Tap to Pay residue.
assert_shipped() {
  local want="$1" label="$2" rc=0
  bash "${GUARD}" "${FIXTURE}" shipped >/dev/null 2>&1 || rc=$?
  if { [ "${want}" = pass ] && [ "${rc}" = 0 ]; } || { [ "${want}" = fail ] && [ "${rc}" != 0 ]; }; then
    echo "PASS (shipped profile, ${want}): ${label}"
  else
    echo "FAIL (shipped profile, wanted ${want}): ${label}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}
write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-feature android:name="android.hardware.location" android:required="false" />
    <uses-feature android:name="android.hardware.location.gps" android:required="false" />
    <uses-permission android:name="android.permission.ACCESS_FINE_LOCATION" android:maxSdkVersion="30" />
    <application android:label="x" />
</manifest>
XML
assert_shipped pass "location capped at API 30, no Application class"

write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-feature android:name="android.hardware.location" android:required="false" />
    <uses-feature android:name="android.hardware.location.gps" android:required="false" />
    <uses-permission android:name="android.permission.ACCESS_FINE_LOCATION" />
    <application android:label="x" />
</manifest>
XML
assert_shipped fail "location without maxSdkVersion"

write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-feature android:name="android.hardware.nfc" android:required="false" />
    <uses-permission android:name="android.permission.NFC" />
    <application android:label="x" />
</manifest>
XML
assert_shipped fail "NFC permission present"

write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <application android:name="com.universaltill.pos.TillApplication" />
</manifest>
XML
assert_shipped fail "TillApplication declared"

# 7. The real, current manifest must itself pass -- proves the guard is
#    actually wired to today's fix, not just to synthetic fixtures.
if bash "${GUARD}" "android/app/src/main/AndroidManifest.xml" >/tmp/guard-out.$$ 2>&1; then
  echo "PASS (correctly accepted): real android/app/src/main/AndroidManifest.xml"
else
  echo "FAIL: real AndroidManifest.xml was rejected"
  cat /tmp/guard-out.$$
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
rm -f /tmp/guard-out.$$

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "guard-android-manifest-features_test: ${FAIL_COUNT} assertion(s) failed" >&2
  exit 1
fi
echo "guard-android-manifest-features_test: all assertions passed"
