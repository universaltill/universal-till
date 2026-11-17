#!/usr/bin/env bash
#
# Release check (ut-docs#5436 / ADR-0179): the signing certificate of a built
# Android APK must be in the pin file the shell compiles in, so we never
# publish an APK our own armed tills would refuse.
#
# Usage: check-apk-signer-pin.sh <pins-file> <flavor> <sha256-digest> [<apk>]
#   <digest> is `apksigner verify --print-certs` "SHA-256 digest"; case and
#   colons are normalised. Parsing mirrors android/app/build.gradle.kts.
#   With <apk>, the signer digest must also appear in the APK's classes*.dex
#   (the BuildConfig constant), proving the build was armed and not made with
#   an empty -PutApkSignerPins override.
set -euo pipefail

if [ "$#" -ne 3 ] && [ "$#" -ne 4 ]; then
  echo "usage: $0 <pins-file> <flavor> <sha256-digest> [<apk>]" >&2
  exit 2
fi
file="$1"
flavor="$2"
raw="$3"
apk="${4:-}"

fail() {
  echo "::error::$*" >&2
  exit 1
}

if [ "${ORG_GRADLE_PROJECT_utApkSignerPins+set}" = set ]; then
  fail "ORG_GRADLE_PROJECT_utApkSignerPins is set: a release must use the committed ${file} (ut-docs#5436)"
fi
[ -f "$file" ] || fail "pin file ${file} is missing (ut-docs#5436)"

pins=()
while IFS= read -r line; do
  line="${line%%#*}"
  line="$(echo "$line" | tr -d '[:space:]')"
  [ -n "$line" ] && pins+=("$line")
done <"$file"

[ "${#pins[@]}" -ge 1 ] || fail "${file} lists no digest (ut-docs#5436)"
[ "${#pins[@]}" -le 4 ] || fail "${file} lists ${#pins[@]} digests, at most 4 allowed (ut-docs#5436)"
for p in "${pins[@]}"; do
  [[ "$p" =~ ^[0-9a-f]{64}$ ]] || fail "${file} entry '${p}' is not 64 lowercase hex characters (ut-docs#5436)"
done
dups="$(printf '%s\n' "${pins[@]}" | sort | uniq -d)"
[ -z "$dups" ] || fail "${file} lists a digest twice (ut-docs#5436)"

[ "$(printf '%s\n' "$raw" | wc -l)" -eq 1 ] || fail "${flavor} APK reported more than one signer certificate digest; multi-signer APKs are refused by tills (ut-docs#5436)"
digest="$(printf '%s' "$raw" | tr -d ': \t' | tr '[:upper:]' '[:lower:]')"
[[ "$digest" =~ ^[0-9a-f]{64}$ ]] || fail "${flavor} signer digest '${raw}' is not a SHA-256 digest (ut-docs#5436)"

pinned=0
for p in "${pins[@]}"; do
  [ "$p" = "$digest" ] && pinned=1
done
[ "$pinned" = 1 ] || fail "${flavor} APK is signed by ${digest}, which ${file} does not pin — tills would refuse this update (ut-docs#5436)"

if [ -n "$apk" ]; then
  [ -f "$apk" ] || fail "${flavor} APK ${apk} is not armed: cannot read it (ut-docs#5436)"
  # grep -c (not -q) so unzip is never killed by SIGPIPE under pipefail.
  hits="$(unzip -p "$apk" 'classes*.dex' | grep -acF "$digest" || true)"
  [ "${hits:-0}" -ge 1 ] || fail "${flavor} APK is not armed: pin digest ${digest} is absent from its classes*.dex, so it was built without the pin set (ut-docs#5436)"
fi
echo "${flavor}: signer ${digest} is pinned"
