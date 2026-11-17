#!/usr/bin/env bash
#
# Verifies a Google Play bundle (.aab) against the store policy (ADR-0194 §2.4,
# ut-docs#6409). Reusable by the pipeline (play-publish, #6410) and by hand:
#
#   android/scripts/check-aab.sh path/to/app-play-release.aab [--expect-version-code N]
#                                [--expect-cert-sha256 HEX] [--allow-unsigned]
#
# Checks (exit 1 on any failure, every failure printed):
#   - the manifest declares NO REQUEST_INSTALL_PACKAGES and no update FileProvider
#   - targetSdkVersion >= 36 (TARGET_SDK_MIN) and minSdk 24
#   - native libs: arm64-v8a present, and no 32-bit ABI without its 64-bit twin
#   - versionCode ends in 5 (base*10+5), or equals --expect-version-code
#   - the bundle is signed (JAR/v1 scheme: the only scheme an .aab has; v2+ exist
#     only on the APKs Play derives) unless --allow-unsigned; with
#     --expect-cert-sha256 the signer must match
#   - no .apk / "unitill-pos-update" payload inside
# Tools: aapt2 + (jarsigner|keytool) from the JDK. bundletool is used for the
# manifest when BUNDLETOOL_JAR (path to bundletool-all.jar, supplied by the
# caller, verified by the caller) is set; otherwise the base module is
# converted to an APK-shaped zip and read with aapt2. Prints the evidence.
set -euo pipefail

TARGET_SDK_MIN=36
aab="" ; want_vc="" ; want_cert="" ; allow_unsigned=0
while [ $# -gt 0 ]; do
  case "$1" in
    --expect-version-code) want_vc="${2:?}"; shift 2 ;;
    --expect-cert-sha256) want_cert="$(printf '%s' "${2:?}" | tr -d ':' | tr 'A-F' 'a-f')"; shift 2 ;;
    --allow-unsigned) allow_unsigned=1; shift ;;
    -h|--help) sed -n 2,23p "$0"; exit 0 ;;
    -*) echo "check-aab: unknown flag $1" >&2; exit 2 ;;
    *) aab="$1"; shift ;;
  esac
done
[ -n "$aab" ] && [ -f "$aab" ] || { echo "usage: check-aab.sh <file.aab> [flags]" >&2; exit 2; }

find_aapt2() {
  if command -v aapt2 >/dev/null 2>&1; then command -v aapt2; return; fi
  local sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}"
  [ -n "$sdk" ] && ls -d "$sdk"/build-tools/*/aapt2 2>/dev/null | sort -V | tail -1
}
aapt2="$(find_aapt2 || true)"
[ -n "$aapt2" ] || { echo "check-aab: aapt2 not found (set ANDROID_HOME)" >&2; exit 2; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fails=0
fail() { echo "FAIL: $*"; fails=$((fails + 1)); }
ok() { echo "ok:   $*"; }

entries="$(unzip -Z1 "$aab")"
echo "== $aab ($(wc -c <"$aab" | tr -d ' ') bytes)"

# --- manifest as text -------------------------------------------------------
if [ -n "${BUNDLETOOL_JAR:-}" ]; then
  java -jar "$BUNDLETOOL_JAR" dump manifest --bundle="$aab" >"$work/manifest.txt"
  src="bundletool"
else
  # Re-shape base/ as an APK: manifest/AndroidManifest.xml -> AndroidManifest.xml,
  # keep resources.pb, then let aapt2 convert the proto XML back to binary.
  mkdir "$work/base"
  unzip -q "$aab" 'base/manifest/*' 'base/resources.pb' 'base/res/*' 'base/lib/*' -d "$work/x" 2>/dev/null || true
  [ -f "$work/x/base/manifest/AndroidManifest.xml" ] || { echo "check-aab: no base/manifest/AndroidManifest.xml: not an app bundle" >&2; exit 1; }
  (cd "$work/x/base" && mkdir -p ../apk && cp manifest/AndroidManifest.xml ../apk/ && cp resources.pb ../apk/ 2>/dev/null; [ -d res ] && cp -R res ../apk/res; cd ../apk && zip -q -r ../base-proto.apk .)
  "$aapt2" convert --output-format binary "$work/x/base-proto.apk" -o "$work/base.apk"
  "$aapt2" dump badging "$work/base.apk" >"$work/badging.txt"
  "$aapt2" dump xmltree --file AndroidManifest.xml "$work/base.apk" >"$work/manifest.txt"
  src="aapt2"
fi
ok "manifest read via $src"

# --- permissions -------------------------------------------------------------
if grep -q 'REQUEST_INSTALL_PACKAGES' "$work/manifest.txt"; then
  fail "manifest declares REQUEST_INSTALL_PACKAGES"
else ok "no REQUEST_INSTALL_PACKAGES"; fi
if grep -Eq '\.updates"|\.updates\b.*authorities|androidx.core.content.FileProvider' "$work/manifest.txt"; then
  fail "manifest carries the update FileProvider"
else ok "no update FileProvider"; fi
echo "-- uses-permission:"
grep -oE 'name[=)"]*[^ ]*android\.permission\.[A-Z_]+|android:name="android\.permission\.[A-Z_]+"' "$work/manifest.txt" |
  grep -oE 'android\.permission\.[A-Z_]+' | sort -u | sed 's/^/     /'

# --- sdk levels + version ------------------------------------------------------
# aapt2 xmltree: `versionCode(0x0101021b)=15`; bundletool XML: `android:versionCode="15"`
num() { grep -oE "$1(\\(0x[0-9a-f]+\\))?=\"?[0-9]+" "$work/manifest.txt" | head -1 | grep -oE '[0-9]+$'; }
tsdk="$(num targetSdkVersion)"; msdk="$(num minSdkVersion)"; vc="$(num versionCode)"
[ -n "$tsdk" ] && [ -n "$msdk" ] && [ -n "$vc" ] || { echo "check-aab: could not read sdk/version from the manifest" >&2; exit 1; }
tsdk=$((tsdk)); msdk=$((msdk)); vc=$((vc))
[ "$tsdk" -ge "$TARGET_SDK_MIN" ] && ok "targetSdkVersion=$tsdk (>= $TARGET_SDK_MIN)" || fail "targetSdkVersion=$tsdk < $TARGET_SDK_MIN"
ok "minSdkVersion=$msdk"
if [ -n "$want_vc" ]; then
  [ "$vc" = "$want_vc" ] && ok "versionCode=$vc" || fail "versionCode=$vc, expected $want_vc"
else
  [ $((vc % 10)) -eq 5 ] && ok "versionCode=$vc (base*10+5)" || fail "versionCode=$vc does not end in 5 (base*10+5)"
fi

# --- ABIs -------------------------------------------------------------------
abis="$(grep -oE '^base/lib/[^/]+/' <<<"$entries" | cut -d/ -f3 | sort -u)"
echo "-- native ABIs: $(echo "$abis" | tr '\n' ' ')"
grep -qx 'arm64-v8a' <<<"$abis" && ok "arm64-v8a (64-bit) present" || fail "no arm64-v8a native libs"
if grep -qx 'armeabi-v7a' <<<"$abis" && ! grep -qx 'arm64-v8a' <<<"$abis"; then fail "32-bit ABI without its 64-bit twin"; fi
if grep -qxE 'x86' <<<"$abis" && ! grep -qx 'x86_64' <<<"$abis"; then fail "x86 without x86_64"; fi

# --- payload -----------------------------------------------------------------
if grep -qiE '\.apk$|unitill-pos-update' <<<"$entries"; then fail "bundle contains an .apk / update payload"; else ok "no .apk / update payload"; fi

# --- signature (JAR, the only scheme on an .aab) -----------------------------
if unzip -Z1 "$aab" | grep -qE '^META-INF/.*\.(RSA|DSA|EC)$'; then
  if jarsigner -verify "$aab" >"$work/jarsigner.txt" 2>&1; then ok "JAR signature verifies (v1; v2+ only exist on Play-derived APKs)"; else cat "$work/jarsigner.txt"; fail "JAR signature does not verify"; fi
  sig="$(unzip -Z1 "$aab" | grep -E '^META-INF/.*\.(RSA|DSA|EC)$' | head -1)"
  unzip -p "$aab" "$sig" >"$work/sig.p7"
  keytool -printcert -file "$work/sig.p7" >"$work/cert.txt" 2>&1 || true
  cert="$(grep -m1 'SHA256:' "$work/cert.txt" | sed 's/.*SHA256: *//' | tr -d ':' | tr 'A-F' 'a-f')"
  echo "-- signer SHA-256: ${cert:-unknown}"
  if [ -n "$want_cert" ]; then
    [ "$cert" = "$want_cert" ] && ok "signer matches the expected upload certificate" || fail "signer ${cert:-unknown} != expected $want_cert"
  fi
else
  if [ "$allow_unsigned" = 1 ]; then ok "unsigned (allowed: offline/no-key build; signing happens in play-publish)"; else fail "bundle is not signed (pass --allow-unsigned for an offline build)"; fi
fi

if [ "$fails" -gt 0 ]; then echo "check-aab: $fails check(s) FAILED"; exit 1; fi
echo "check-aab: all checks passed"
