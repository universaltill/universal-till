#!/usr/bin/env bash
#
# Self-test for check-apk-signer-pin.sh (ut-docs#5436 / ADR-0179). Fixtures
# only in a fresh mktemp dir, never this checkout.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/ci/check-apk-signer-pin.sh"
fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

A="$(printf 'a%.0s' $(seq 64))"
B="$(printf 'b%.0s' $(seq 64))"
C="$(printf 'c%.0s' $(seq 64))"
D="$(printf 'd%.0s' $(seq 64))"
E="$(printf 'e%.0s' $(seq 64))"

# run <pins-file> <digest> [env assignment...] -> OUT, ERR, RC
run() {
  local file="$1" digest="$2"
  shift 2
  set +e
  OUT="$(env "$@" bash "$SCRIPT" "$file" arm64 "$digest" ${APK:+"$APK"} 2>"$T/err")"
  RC=$?
  set -e
  ERR="$(cat "$T/err")"
}
ok_case() { # name file digest
  run "$2" "$3"
  if [ "$RC" = 0 ] && grep -q 'arm64: signer .* is pinned' <<<"$OUT"; then pass "$1"; else fail "$1 (rc=$RC out=$OUT err=$ERR)"; fi
}
bad_case() { # name file digest [env...]
  local name="$1" file="$2" digest="$3"
  shift 3
  run "$file" "$digest" "$@"
  if [ "$RC" != 0 ] && grep -q '::error::' <<<"$ERR"; then pass "$name"; else fail "$name (rc=$RC out=$OUT err=$ERR)"; fi
}

bad_msg() { # name want-regex file digest [env...]
  local name="$1" want="$2"
  shift 2
  bad_case "$name" "$@"
  if grep -qE "$want" <<<"$ERR"; then pass "$name: reason"; else fail "$name: wrong reason (want /$want/, err=$ERR)"; fi
}
# mkapk <path> <dex-content>: a zip with classes.dex and classes2.dex
mkapk() {
  local dir="$T/apk.$RANDOM"
  mkdir -p "$dir"
  printf 'dex\n035\0%s\0tail\n' "$2" >"$dir/classes2.dex"
  printf 'dex\n035\0nothing\0\n' >"$dir/classes.dex"
  (cd "$dir" && zip -q "$1" classes.dex classes2.dex)
}

printf '# comment\n%s  # trailing\n\n' "$A" >"$T/one"
printf '%s\n%s\n' "$A" "$B" >"$T/two"
printf '%s\n%s\n%s\n%s\n%s\n' "$A" "$B" "$C" "$D" "$E" >"$T/five"
printf '%s\nnot-hex\n' "$A" >"$T/malformed"
printf '%s\n' "$(printf 'A%.0s' $(seq 64))" >"$T/upper"
printf '%s\n%s\n' "$A" "$A" >"$T/dup"
printf '# nothing here\n\n' >"$T/comments"
: >"$T/empty"

ok_case "listed digest passes" "$T/one" "$A"
ok_case "second entry of two matches" "$T/two" "$B"
UPPER_COLON="$(printf 'AA:%.0s' $(seq 31))AA"
ok_case "uppercase colon digest normalises" "$T/one" "$UPPER_COLON"
bad_case "unlisted digest fails" "$T/one" "$B"
if grep -q "does not pin" <<<"$ERR" && grep -q "$B" <<<"$ERR"; then pass "unlisted: message names the digest"; else fail "unlisted message ($ERR)"; fi
bad_msg "malformed entry fails" "not 64 lowercase hex" "$T/malformed" "$A"
bad_msg "uppercase entry in file fails" "not 64 lowercase hex" "$T/upper" "$A"
bad_msg "duplicate entries fail" "twice" "$T/dup" "$A"
bad_msg "five entries fail" "at most 4" "$T/five" "$A"
bad_msg "comment-only file fails" "lists no digest" "$T/comments" "$A"
bad_msg "empty file fails" "lists no digest" "$T/empty" "$A"
bad_msg "missing file fails" "is missing" "$T/nope" "$A"
bad_msg "empty digest fails" "not a SHA-256 digest" "$T/one" ""
bad_msg "multi-line digest fails" "more than one signer" "$T/one" "$(printf '%s\n%s' "$A" "$A")"
bad_msg "override env set fails" "utApkSignerPins is set" "$T/one" "$A" ORG_GRADLE_PROJECT_utApkSignerPins="$A"
bad_msg "override env set but empty also fails" "utApkSignerPins is set" "$T/one" "$A" ORG_GRADLE_PROJECT_utApkSignerPins=

# L1: the APK itself must carry the digest (built armed, not with an empty pin override).
mkapk "$T/armed.apk" "x,$A,y"
mkapk "$T/dormant.apk" "no pin here"
APK="$T/armed.apk"
ok_case "armed APK (digest in classes2.dex) passes" "$T/one" "$A"
APK="$T/dormant.apk"
bad_msg "dormant APK (digest absent from dex) fails" "not armed" "$T/one" "$A"
APK="$T/missing.apk"
bad_msg "missing APK file fails" "not armed|cannot read" "$T/one" "$A"
APK=""

if [ "$fails" -ne 0 ]; then
  echo "$fails case(s) failed" >&2
  exit 1
fi
echo "check-apk-signer-pin: all cases passed"
