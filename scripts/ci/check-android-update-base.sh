#!/usr/bin/env bash
#
# ut-docs#5271: android/app/build.gradle.kts must refuse a malformed
# -PupdateApkUrlBase at configuration time (never fall back to our releases)
# and accept the well-formed ones. Needs the Android SDK, so it runs in
# android-ci.yml only; `help` just configures, so each case takes seconds.
#
# Usage (from android/): check-android-update-base.sh ./gradlew
set -uo pipefail

GRADLEW="${1:-./gradlew}"
fail=0

run() {
  "${GRADLEW}" -q help "-PupdateApkUrlBase=$1" 2>&1
}

bad=(
  ""
  "http://example.test/apk/"
  "https://example.test/apk"
  "https://Example.test/apk/"
  "https://example.test./apk/"
  "https://user@example.test/apk/"
  "https://example.test/apk/?x=1/"
  "https://example.test/a/../b/"
  "https://example.test/a/%2e%2e/"
  "https://example.test:0/a/"
  "https://example.test/a b/"
  'https://example.test/a"/'
  "https://github.com/o/r/releases/download/v1/"
  "https://objects.githubusercontent.com/x/"
  "https://api.github.com/repos/o/r/releases/"
)
good=(
  "https://github.com/universaltill/universal-till/releases/latest/download/"
  "https://github.com/acme-co/Till.fork/releases/latest/download/"
  "https://updates.example.test/"
  "https://updates.example.test:8443/apk/v/"
)

for v in "${bad[@]}"; do
  if out="$(run "$v")"; then
    echo "FAIL: '${v}' was accepted"
    fail=1
  elif ! grep -q 'updateApkUrlBase must be' <<<"$out"; then
    echo "FAIL: '${v}' failed for another reason:"
    echo "$out" | tail -5
    fail=1
  else
    echo "PASS (rejected): '${v}'"
  fi
done
for v in "${good[@]}"; do
  if out="$(run "$v")"; then
    echo "PASS (accepted): '${v}'"
  else
    echo "FAIL: '${v}' was rejected:"
    echo "$out" | tail -5
    fail=1
  fi
done
"${GRADLEW}" -q --stop >/dev/null 2>&1 || true
exit "${fail}"
