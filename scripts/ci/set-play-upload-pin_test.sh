#!/usr/bin/env bash
# shellcheck disable=SC2015
# Self-test for set-play-upload-pin.sh (ut-docs#6410) with a fake keytool.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
S="$ROOT/scripts/ci/set-play-upload-pin.sh"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
fails=0
ok() { echo "ok   - $1"; }
bad() { echo "FAIL - $1"; fails=$((fails + 1)); }
REL="$(printf 'a%.0s' {1..64})"
UPCOL="BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB:BB"
UP="$(printf 'b%.0s' {1..64})"
RELCOL="AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA:AA"
PASS="s3cret-SENTINEL-pw"
mkdir "$tmp/bin"
cat >"$tmp/bin/keytool" <<'KT'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$KT_LOG"
[ "${STOREPASS:-}" = "$FAKE_PASS" ] || { echo "keytool error: Keystore was tampered with, or password was incorrect" >&2; exit 1; }
case " $* " in *" -v "*)
  echo "Certificate fingerprints:"; echo "	 SHA1: 11:22"; echo "	 SHA256: $FAKE_SHA"; exit 0 ;;
esac
IFS=, read -ra al <<<"${FAKE_ALIASES:-upload}"
for a in "${al[@]}"; do echo "$a, 1 Jan 2026, PrivateKeyEntry,"; done
KT
chmod +x "$tmp/bin/keytool"
: >"$tmp/ks"
export KT_LOG="$tmp/kt.log" FAKE_PASS="$PASS" FAKE_SHA="$UPCOL" KEYTOOL="$tmp/bin/keytool"
mk() { rm -rf "$tmp/d"; mkdir "$tmp/d"; printf '# c\n%s\n' "$REL" >"$tmp/d/apk-signer-sha256.txt"; printf '# h1\n# h2\n' >"$tmp/d/play-upload-cert-sha256.txt"; : >"$KT_LOG"; }
run() { local rc=0 o; o="$(UT_PIN_DIR="$tmp/d" UT_PLAY_UPLOAD_STOREPASS="${PW-$PASS}" bash "$S" "$@" 2>&1 </dev/null)" || rc=$?; echo "$rc|$o"; }
P="$tmp/d/play-upload-cert-sha256.txt"

mk; r="$(run "$tmp/ks")"
[ "${r%%|*}" = 0 ] && ok "happy path exits 0" || bad "happy: $r"
[ "$(grep -c "^$UP$" "$P")" = 1 ] && ok "pin written (lowercase, no colons)" || bad "pin content: $(cat "$P")"
[ "$(grep -c '^# h' "$P")" = 2 ] && ok "header lines preserved" || bad "header lost"
case "$r" in *"$UP"*"play-publish.yml"*) ok "prints digest and next steps" ;; *) bad "next steps: $r" ;; esac
case "$r" in *"$PASS"*) bad "password printed" ;; *) ok "password not in output" ;; esac
grep -q "$PASS" "$KT_LOG" && bad "password in keytool argv" || ok "password not in keytool argv"
grep -q -- '-storepass:env' "$KT_LOG" && ok "keytool gets -storepass:env" || bad "no -storepass:env"
sum1="$(cksum <"$P")"; r="$(run "$tmp/ks")"; [ "${r%%|*}" = 0 ] && [ "$sum1" = "$(cksum <"$P")" ] && ok "idempotent" || bad "idempotent: $r"

mk; r="$(FAKE_SHA="$RELCOL" run "$tmp/ks")"; before="$(cksum <"$P")"
[ "${r%%|*}" = 3 ] && ok "release cert refused (exit 3)" || bad "release refusal: $r"
case "$r" in *"RELEASE cert; create a separate upload key"*) ok "release refusal message" ;; *) bad "release msg: $r" ;; esac
[ "$before" = "$(cksum <"$P")" ] && ok "file untouched after refusal" || bad "file changed after refusal"

mk; r="$(PW=wrong run "$tmp/ks")"
[ "${r%%|*}" = 4 ] && ok "wrong password exits 4" || bad "wrong pw: $r"
[ "$(wc -l <<<"$r")" -le 2 ] && ok "wrong password: one-line error" || bad "multi-line error: $r"
[ "$(grep -c "^$UP$" "$P" || true)" = 0 ] && ok "wrong password leaves file untouched" || bad "file written on wrong pw"

mk; r="$(UT_PIN_DIR="$tmp/d" env -u UT_PLAY_UPLOAD_STOREPASS bash "$S" "$tmp/ks" 2>&1 </dev/null)" && rc=0 || rc=$?
[ "$rc" = 2 ] && ok "no tty + no env exits 2" || bad "no tty: rc=$rc $r"

mk; run "$tmp/ks" >/dev/null; printf '# h1\n# h2\n%s\n' "$(printf 'c%.0s' {1..64})" >"$P"
r="$(run "$tmp/ks")"
[ "${r%%|*}" = 3 ] && ok "different existing pin needs --replace" || bad "replace guard: $r"
case "$r" in *"old "*"new $UP"*"--replace"*) ok "guard prints old/new" ;; *) bad "guard msg: $r" ;; esac
r="$(run --replace "$tmp/ks")"
[ "${r%%|*}" = 0 ] && [ "$(grep -c "^$UP$" "$P")" = 1 ] && [ "$(grep -cE '^[0-9a-f]{64}$' "$P")" = 1 ] && ok "--replace swaps the pin" || bad "replace: $r"

mk; rm "$tmp/d/apk-signer-sha256.txt"; r="$(run "$tmp/ks")"
[ "${r%%|*}" = 3 ] && ok "missing apk-signer file refused" || bad "missing signer: $r"

mk; r="$(FAKE_ALIASES="one,two" run "$tmp/ks")"
[ "${r%%|*}" = 2 ] && ok "several aliases without alias refused" || bad "multi alias: $r"
r="$(FAKE_ALIASES="one,two" run "$tmp/ks" two)"; [ "${r%%|*}" = 0 ] && ok "explicit alias accepted" || bad "explicit alias: $r"

r="$(run "$tmp/nope")"; [ "${r%%|*}" = 4 ] && ok "missing keystore exits 4" || bad "missing keystore: $r"
[ "$fails" = 0 ]
