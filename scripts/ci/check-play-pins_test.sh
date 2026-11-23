#!/usr/bin/env bash
# shellcheck disable=SC2015
# Self-test for check-play-pins.sh (ut-docs#6410): the shared pin pre-check used
# by the play-publish build job (warn-only on a dry run) and the sign job.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
S="$ROOT/scripts/ci/check-play-pins.sh"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
fails=0
ok() { echo "ok   - $1"; }
bad() { echo "FAIL - $1"; fails=$((fails + 1)); }
REL="$(printf 'a%.0s' {1..64})"; UP="$(printf 'b%.0s' {1..64})"
# run <dir> [args]: prints "rc|output"
run() { local d="$1"; shift; local o rc=0; o="$(UT_PIN_DIR="$d" bash "$S" "$@" 2>&1)" || rc=$?; echo "$rc|$o"; }
mk() { rm -rf "$tmp/d"; mkdir "$tmp/d"; printf '# c\n%s\n' "$REL" >"$tmp/d/apk-signer-sha256.txt"; printf '# header\n%s\n' "${1:-}" >"$tmp/d/play-upload-cert-sha256.txt"; [ -n "${1:-}" ] || printf '# header\n' >"$tmp/d/play-upload-cert-sha256.txt"; }

mk "$UP"; r="$(run "$tmp/d")"; [ "${r%%|*}" = 0 ] && ok "valid pin passes" || bad "valid pin: $r"
r="$(run "$tmp/d" --warn-only)"; [ "${r%%|*}" = 0 ] && ok "valid pin passes warn-only" || bad "valid pin warn-only: $r"

mk ""; r="$(run "$tmp/d")"
[ "${r%%|*}" = 1 ] && ok "empty pin fails a real run" || bad "empty pin real: $r"
case "$r" in *"::error::"*"set-play-upload-pin.sh <upload.keystore>"*"runbook step E"*) ok "empty pin names the owner command" ;; *) bad "empty pin message: $r" ;; esac
r="$(run "$tmp/d" --warn-only)"
[ "${r%%|*}" = 0 ] && ok "empty pin only warns on a dry run" || bad "empty pin warn-only rc: $r"
case "$r" in *"::warning::"*"set-play-upload-pin.sh"*) ok "dry run warning names the script" ;; *) bad "warning message: $r" ;; esac
case "$r" in *"::error::"*) bad "warn-only emitted an error" ;; *) ok "warn-only emits no error" ;; esac

mk "$REL"; for a in "" --warn-only; do
  r="$(run "$tmp/d" $a)"
  [ "${r%%|*}" = 1 ] && ok "pin equal to release cert fails (${a:-real})" || bad "release pin ${a:-real}: $r"
  case "$r" in *"::error::"*"SEPARATE upload keystore"*"set-play-upload-pin.sh --replace <upload.keystore>"*) ok "release-equal message names the command" ;; *) bad "release message: $r" ;; esac
done

mk "$UP"; : >"$tmp/d/apk-signer-sha256.txt"
for a in "" --warn-only; do
  r="$(run "$tmp/d" $a)"
  [ "${r%%|*}" = 1 ] && ok "empty apk-signer file fails (${a:-real})" || bad "empty signer ${a:-real}: $r"
  case "$r" in *"::error::"*"apk-signer-sha256.txt is missing or empty"*"git checkout main -- android/app/apk-signer-sha256.txt"*) ok "missing-list message says restore" ;; *) bad "signer message: $r" ;; esac
done
rm "$tmp/d/apk-signer-sha256.txt"; r="$(run "$tmp/d")"; [ "${r%%|*}" = 1 ] && ok "missing apk-signer file fails" || bad "missing signer: $r"

mk "$UP"; printf '# only comments\n# no digest here\n' >"$tmp/d/apk-signer-sha256.txt"
for a in "" --warn-only; do
  r="$(run "$tmp/d" $a)"
  [ "${r%%|*}" = 1 ] && ok "comment-only apk-signer list fails (${a:-real})" || bad "comment-only signer ${a:-real}: $r"
  case "$r" in *"::error::"*"apk-signer-sha256.txt"*"no release"*) ok "comment-only message names the file" ;; *) bad "comment-only message: $r" ;; esac
done

r="$(run "$tmp/d" --bogus)"; [ "${r%%|*}" = 2 ] && ok "unknown flag is a usage error" || bad "bogus flag: $r"
[ "$fails" = 0 ]
