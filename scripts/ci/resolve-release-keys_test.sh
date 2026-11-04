#!/usr/bin/env bash
#
# Self-test for resolve-release-keys.sh (ADR-0178): the release public keys
# compiled into every release build. Runs the script with fixtures only in a
# fresh mktemp dir, never this checkout.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/ci/resolve-release-keys.sh"
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

# run <pin> [github_env]  -> OUT (stdout), ERR (stderr), RC
run() {
  set +e
  OUT="$(env UT_RELEASE_PUBLIC_KEYS_PIN="$1" GITHUB_ENV="${2:-}" bash "$SCRIPT" 2>"$T/err")"
  RC=$?
  set -e
  ERR="$(cat "$T/err")"
}

expect_ok() { # name pin
  run "$2"
  if [ "$RC" = 0 ] && [ "$OUT" = "$2" ] && ! grep -q '::' <<<"$ERR"; then pass "$1"; else fail "$1 (rc=$RC out=$OUT err=$ERR)"; fi
}
expect_bad() { # name pin
  run "$2"
  if [ "$RC" != 0 ] && grep -q '::error::' <<<"$ERR" && [ -z "$OUT" ]; then pass "$1"; else fail "$1 (rc=$RC out=$OUT err=$ERR)"; fi
}

run ""
if [ "$RC" = 0 ] && [ -z "$OUT" ] && grep -q '::warning::release signing is not configured' <<<"$ERR"; then pass "empty: warns, exports empty"; else fail "empty (rc=$RC out=$OUT err=$ERR)"; fi

: >"$T/env"
run "" "$T/env"
if [ "$RC" = 0 ] && [ "$(cat "$T/env")" = "UT_RELEASE_KEYS_HEX=" ]; then pass "empty: UT_RELEASE_KEYS_HEX= is still exported to GITHUB_ENV"; else fail "empty GITHUB_ENV ($(cat "$T/env"))"; fi

: >"$T/env"
run "$A,$B" "$T/env"
if [ "$RC" = 0 ] && [ "$(cat "$T/env")" = "UT_RELEASE_KEYS_HEX=$A,$B" ]; then pass "two keys: exported to GITHUB_ENV"; else fail "two keys GITHUB_ENV ($(cat "$T/env"))"; fi

run "$A"
if [ "$RC" = 0 ] && [ "$OUT" = "$A" ] && grep -q '::warning::.*one key' <<<"$ERR" && ! grep -q '::error::' <<<"$ERR"; then pass "one key: accepted with a warning (ADR-0178 s6 wants A,B)"; else fail "one key (rc=$RC out=$OUT err=$ERR)"; fi
expect_ok "two keys" "$A,$B"
expect_ok "four keys" "$A,$B,$C,$D"
expect_bad "uppercase" "$(tr a-f A-F <<<"$A")"
expect_bad "63 hex" "${A:1}"
expect_bad "duplicate" "$A,$A"
expect_bad "five keys" "$A,$B,$C,$D,$E"
expect_bad "embedded space" "$A, $B"
expect_bad "trailing comma" "$A,"

[ "$fails" -eq 0 ] || { echo "$fails failure(s)" >&2; exit 1; }
echo "all resolve-release-keys checks passed"
