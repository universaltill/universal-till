#!/usr/bin/env bash
#
# Regression test for check-aab.sh's native-debug-symbols check (ut-docs#6935):
# a Play bundle whose base/lib/<abi>/<lib>.so has no matching
# BUNDLE-METADATA/com.android.tools.build.debugsymbols/<abi>/<lib>.so.sym (or
# .so.dbg) must FAIL, one with them must pass. aapt2 is a stub on PATH and the
# bundles are synthetic zips, so this needs only bash, python3 and zip/unzip
# (no Android SDK). Everything is written under one mktemp dir.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="${HERE}/check-aab.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
mkdir "${WORK}/bin"

# Stub aapt2: `convert` just creates the -o target; `dump xmltree` prints a
# canned play-flavor manifest; `dump badging` prints nothing.
cat >"${WORK}/bin/aapt2" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "convert "*)
    while [ $# -gt 0 ]; do
      if [ "$1" = "-o" ]; then : >"$2"; fi
      shift
    done ;;
  "dump xmltree")
    cat <<'XML'
N: android=http://schemas.android.com/apk/res/android
  E: manifest
      A: http://schemas.android.com/apk/res/android:versionCode(0x0101021b)=10005
      E: uses-sdk
        A: http://schemas.android.com/apk/res/android:minSdkVersion(0x0101020c)=24
        A: http://schemas.android.com/apk/res/android:targetSdkVersion(0x01010270)=36
      E: uses-permission
        A: http://schemas.android.com/apk/res/android:name(0x01010003)="android.permission.INTERNET"
      E: application
        E: meta-data
          A: http://schemas.android.com/apk/res/android:name(0x01010003)="com.universaltill.pos.install_channel"
          A: http://schemas.android.com/apk/res/android:value(0x01010024)="play"
XML
    ;;
esac
exit 0
STUB
chmod +x "${WORK}/bin/aapt2"

SYM="BUNDLE-METADATA/com.android.tools.build.debugsymbols"

# make_aab <out> <extra entry>...  (base module with both ABIs + extras)
make_aab() {
  local out="$1"; shift
  python3 -I - "${out}" "$@" <<'PY'
import sys, zipfile
out, extra = sys.argv[1], sys.argv[2:]
base = ["base/manifest/AndroidManifest.xml", "base/resources.pb",
        "base/lib/arm64-v8a/libgojni.so", "base/lib/armeabi-v7a/libgojni.so"]
with zipfile.ZipFile(out, "w") as z:
    for n in base + extra:
        z.writestr(n, "x")
PY
}

FAIL_COUNT=0
run_case() { # <label> <expect-exit> <expect-text|-> <aab>
  local label="$1" want="$2" text="$3" aab="$4" got=0
  PATH="${WORK}/bin:${PATH}" bash "${CHECK}" "${aab}" --allow-unsigned >"${WORK}/out.txt" 2>&1 || got=$?
  if [ "${got}" != "${want}" ]; then
    echo "FAIL: ${label}: exit ${got}, want ${want}"; sed 's/^/    /' "${WORK}/out.txt"
    FAIL_COUNT=$((FAIL_COUNT + 1)); return
  fi
  if [ "${text}" != "-" ] && ! grep -qF -- "${text}" "${WORK}/out.txt"; then
    echo "FAIL: ${label}: output lacks '${text}'"; sed 's/^/    /' "${WORK}/out.txt"
    FAIL_COUNT=$((FAIL_COUNT + 1)); return
  fi
  echo "PASS: ${label}"
}

make_aab "${WORK}/a.aab" "${SYM}/arm64-v8a/libgojni.so.sym" "${SYM}/armeabi-v7a/libgojni.so.sym"
run_case "symbols for both ABIs (.so.sym) pass" 0 "native debug symbols" "${WORK}/a.aab"

make_aab "${WORK}/b.aab"
run_case "no symbols fail" 1 "no native debug symbols for arm64-v8a/libgojni.so" "${WORK}/b.aab"

make_aab "${WORK}/c.aab" "${SYM}/arm64-v8a/libgojni.so.sym"
run_case "only arm64 symbols fail, naming armeabi-v7a" 1 "no native debug symbols for armeabi-v7a/libgojni.so" "${WORK}/c.aab"

make_aab "${WORK}/d.aab" "${SYM}/arm64-v8a/libgojni.so.dbg" "${SYM}/armeabi-v7a/libgojni.so.dbg"
run_case "symbols as .so.dbg are accepted" 0 "-" "${WORK}/d.aab"

if [ "${FAIL_COUNT}" -gt 0 ]; then echo "check-aab_test: ${FAIL_COUNT} case(s) failed"; exit 1; fi
echo "check-aab_test: all cases passed"
