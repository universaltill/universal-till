#!/usr/bin/env bash
#
# Regression test for guard-android-network-state.sh (ut-docs#3088): proves
# the guard rejects each half of the original bug -- the missing permission
# and the missing setNetworkAvailable call -- rather than merely passing on
# the fixed tree. Runs against throwaway fixtures, never tracked source.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-android-network-state.sh"
TMP="$(mktemp -d -t guard-android-network-state-XXXXXX)"
MANIFEST="${TMP}/AndroidManifest.xml"
ACTIVITY="${TMP}/MainActivity.kt"
OUT="${TMP}/out"
cleanup() { rm -rf "${TMP}"; }
trap cleanup EXIT

FAIL_COUNT=0

assert_passes() {
  if bash "${GUARD}" "${MANIFEST}" "${ACTIVITY}" >"${OUT}" 2>&1; then
    echo "PASS (correctly accepted): $1"
  else
    echo "FAIL (should have been accepted but was rejected): $1"
    cat "${OUT}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

assert_fails() {
  if bash "${GUARD}" "${MANIFEST}" "${ACTIVITY}" >"${OUT}" 2>&1; then
    echo "FAIL (should have been rejected but was accepted): $1"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "PASS (correctly rejected): $1"
  fi
}

good_manifest() {
  cat > "${MANIFEST}" <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-permission android:name="android.permission.INTERNET" />
    <uses-permission android:name="android.permission.ACCESS_NETWORK_STATE" />
</manifest>
XML
}

good_activity() {
  cat > "${ACTIVITY}" <<'KT'
class MainActivity {
    fun apply(up: Boolean) {
        webView.setNetworkAvailable(up)
    }
}
KT
}

# 1. The fixed shape passes.
good_manifest; good_activity
assert_passes "permission declared and setNetworkAvailable called"

# 2. The original bug, half one: no ACCESS_NETWORK_STATE.
cat > "${MANIFEST}" <<'XML'
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-permission android:name="android.permission.INTERNET" />
</manifest>
XML
good_activity
assert_fails "ACCESS_NETWORK_STATE missing from the manifest"

# 3. Half two: the permission is there but nothing tells the WebView.
good_manifest
cat > "${ACTIVITY}" <<'KT'
class MainActivity {
    fun apply(up: Boolean) {}
}
KT
assert_fails "setNetworkAvailable never called"

# 4. A call that survives only in comments does not count.
cat > "${ACTIVITY}" <<'KT'
class MainActivity {
    /**
     * webView.setNetworkAvailable(up) used to live here.
     */
    fun apply(up: Boolean) {
        // webView.setNetworkAvailable(up)
    }
}
KT
assert_fails "setNetworkAvailable only in a KDoc and a line comment"

# 5. A same-named function that is declared but never called on the
#    WebView does not count.
cat > "${ACTIVITY}" <<'KT'
class MainActivity {
    fun setNetworkAvailable(up: Boolean) {}
}
KT
assert_fails "setNetworkAvailable only declared, never called on a receiver"

# 6. A URL's "//" earlier on the same line is not a comment.
cat > "${ACTIVITY}" <<'KT'
class MainActivity {
    fun apply(up: Boolean) {
        val u = "http://127.0.0.1/"; webView.setNetworkAvailable(up)
    }
}
KT
assert_passes "call on a line that also holds a URL"

# 7. The real tree must pass.
if bash "${GUARD}" >"${OUT}" 2>&1; then
  echo "PASS (correctly accepted): real manifest + MainActivity.kt"
else
  echo "FAIL: real manifest / MainActivity.kt rejected"
  cat "${OUT}"
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "guard-android-network-state_test: ${FAIL_COUNT} assertion(s) failed" >&2
  exit 1
fi
echo "guard-android-network-state_test: all assertions passed"
