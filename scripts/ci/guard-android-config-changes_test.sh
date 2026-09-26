#!/usr/bin/env bash
#
# Regression test for guard-android-config-changes.sh (ut-docs#2788):
# proves the guard rejects the shape of the original bug -- MainActivity's
# android:configChanges missing a background-triggered config change
# (keyboard / navigation / uiMode), which recreates the Activity and
# reloads the whole till page with no operator action -- and the unsafe
# variant of the fix (handling uiMode while shipping -night resources).
#
# Runs against throwaway fixtures inside a fresh mktemp -d, never the real
# manifest (except the final "real manifest passes" check, read-only).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-android-config-changes.sh"
WORK="$(mktemp -d -t guard-android-config-changes-XXXXXX)"
cleanup() { rm -rf "${WORK:?}"; }
trap cleanup EXIT

FIXTURE="${WORK}/AndroidManifest.xml"
RES="${WORK}/res"
mkdir -p "${RES}/values"
OUT="${WORK}/out.txt"

FAIL_COUNT=0

write_fixture() {
  cat > "${FIXTURE}"
}

activity_with() {
  write_fixture <<XML
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <application>
        <activity android:name=".MainActivity" android:configChanges="$1" />
    </application>
</manifest>
XML
}

assert_passes() {
  local label="$1"
  if bash "${GUARD}" "${FIXTURE}" "${RES}" >"${OUT}" 2>&1; then
    echo "PASS (correctly accepted): ${label}"
  else
    echo "FAIL (should have been accepted but was rejected): ${label}"
    cat "${OUT}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

assert_fails() {
  local label="$1"
  if bash "${GUARD}" "${FIXTURE}" "${RES}" >"${OUT}" 2>&1; then
    echo "FAIL (should have been rejected but was accepted): ${label}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "PASS (correctly rejected): ${label}"
  fi
}

FULL="orientation|screenSize|smallestScreenSize|screenLayout|keyboardHidden|keyboard|navigation|uiMode"

# 1. The original bug: rotation handled, keyboard/navigation/uiMode not.
activity_with "orientation|screenSize|smallestScreenSize|screenLayout|keyboardHidden"
assert_fails "configChanges without keyboard|navigation|uiMode (the ut-docs#2788 state)"

# 2. Each missing value on its own is caught, not just the whole set.
for drop in orientation screenSize smallestScreenSize screenLayout keyboardHidden keyboard navigation uiMode; do
  activity_with "$(echo "${FULL}" | tr '|' '\n' | grep -vx "${drop}" | paste -sd '|' -)"
  assert_fails "configChanges missing ${drop}"
done

# 3. No configChanges attribute at all.
write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <application>
        <activity android:name=".MainActivity" />
    </application>
</manifest>
XML
assert_fails "MainActivity with no configChanges attribute"

# 4. No MainActivity at all (renamed/moved) must fail loudly, not pass vacuously.
write_fixture <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <application>
        <activity android:name=".OtherActivity" android:configChanges="keyboard" />
    </application>
</manifest>
XML
assert_fails "manifest without a MainActivity"

# 5. The fix: every required value, in any order, extra values allowed.
activity_with "${FULL}"
assert_passes "configChanges with the full required set"
activity_with "uiMode|navigation|keyboard|keyboardHidden|screenLayout|smallestScreenSize|screenSize|orientation|density"
assert_passes "required set in another order plus an extra value"

# 6. uiMode handled in-process + a -night resource dir = night resources
#    would silently never apply. Must fail.
mkdir -p "${RES}/values-night"
activity_with "${FULL}"
assert_fails "uiMode handled while res/values-night exists"
rm -rf "${RES:?}/values-night"
mkdir -p "${RES}/drawable-night-hdpi"
assert_fails "uiMode handled while res/drawable-night-hdpi exists"
rm -rf "${RES:?}/drawable-night-hdpi"
assert_passes "night resources removed again"

# 7. The real, current manifest + res tree must pass.
if bash "${GUARD}" >"${OUT}" 2>&1; then
  echo "PASS (correctly accepted): real android/app/src/main/AndroidManifest.xml"
else
  echo "FAIL: real AndroidManifest.xml was rejected"
  cat "${OUT}"
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "guard-android-config-changes_test: ${FAIL_COUNT} assertion(s) failed" >&2
  exit 1
fi
echo "guard-android-config-changes_test: all assertions passed"
