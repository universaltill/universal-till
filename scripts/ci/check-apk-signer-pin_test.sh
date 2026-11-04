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
  OUT="$(env "$@" bash "$SCRIPT" "$file" arm64 "$digest" 2>"$T/err")"
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
bad_case "malformed entry fails" "$T/malformed" "$A"
bad_case "uppercase entry in file fails" "$T/upper" "$A"
bad_case "duplicate entries fail" "$T/dup" "$A"
bad_case "five entries fail" "$T/five" "$A"
bad_case "comment-only file fails" "$T/comments" "$A"
bad_case "empty file fails" "$T/empty" "$A"
bad_case "missing file fails" "$T/nope" "$A"
bad_case "empty digest fails" "$T/one" ""
bad_case "multi-line digest fails" "$T/one" "$(printf '%s\n%s' "$A" "$A")"
bad_case "override env set fails" "$T/one" "$A" ORG_GRADLE_PROJECT_utApkSignerPins="$A"
bad_case "override env set but empty also fails" "$T/one" "$A" ORG_GRADLE_PROJECT_utApkSignerPins=

if [ "$fails" -ne 0 ]; then
  echo "$fails case(s) failed" >&2
  exit 1
fi
echo "check-apk-signer-pin: all cases passed"
