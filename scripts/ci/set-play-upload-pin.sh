#!/usr/bin/env bash
# Owner tool (ut-docs#6410, ADR-0194): pin the Play UPLOAD certificate.
#   bash scripts/ci/set-play-upload-pin.sh [--replace] <keystore> [alias]
# Reads the SHA-256 of the upload cert with keytool and writes it into
# android/app/play-upload-cert-sha256.txt (public value, not a secret).
# Password: env UT_PLAY_UPLOAD_STOREPASS, else a silent prompt on the terminal.
# It reaches keytool only through the environment (-storepass:env), never argv,
# never printed, never written. No `set -x` here, keep it that way.
# Refuses the RELEASE cert. Exit: 0 ok, 2 usage/env, 3 refusal, 4 keytool failure.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PIN_DIR="${UT_PIN_DIR:-$ROOT/android/app}"
KEYTOOL="${KEYTOOL:-keytool}"
die() { echo "$2" >&2; exit "$1"; }
replace=0
if [ "${1:-}" = "--replace" ]; then replace=1; shift; fi
ks="${1:-}"; alias_name="${2:-}"
[ -n "$ks" ] || die 2 "usage: set-play-upload-pin.sh [--replace] <keystore> [alias]"
[ -f "$ks" ] && [ -r "$ks" ] || die 4 "cannot read keystore: $ks"
pass="${UT_PLAY_UPLOAD_STOREPASS:-}"
if [ -z "$pass" ]; then
  [ -t 0 ] || die 2 "no password: set UT_PLAY_UPLOAD_STOREPASS or run on a terminal"
  IFS= read -rsp "Keystore password: " pass; echo >&2
  [ -n "$pass" ] || die 2 "empty password"
fi
kt() { STOREPASS="$pass" "$KEYTOOL" "$@" -storepass:env STOREPASS 2>/dev/null; }
command -v "$KEYTOOL" >/dev/null 2>&1 || die 4 "keytool not found (install a JDK)"
listing="$(kt -list -keystore "$ks")" || die 4 "keytool failed: wrong password, corrupt or unreadable keystore"
if [ -z "$alias_name" ]; then
  n="$(grep -c 'PrivateKeyEntry' <<<"$listing" || true)"
  [ "$n" = 1 ] || die 2 "keystore has $n key entries; pass the alias"
  alias_name="$(awk -F, '/PrivateKeyEntry/{print $1; exit}' <<<"$listing")"
fi
detail="$(kt -list -v -keystore "$ks" -alias "$alias_name")" || die 4 "keytool failed: unknown alias or unreadable keystore"
digest="$(awk '/SHA256:/{print $2; exit}' <<<"$detail" | tr -d ':' | tr 'A-F' 'a-f')"
[[ "$digest" =~ ^[0-9a-f]{64}$ ]] || die 4 "could not read a SHA-256 fingerprint from keytool output"
rel_file="$PIN_DIR/apk-signer-sha256.txt"
pin_file="$PIN_DIR/play-upload-cert-sha256.txt"
if [ ! -s "$rel_file" ] || ! grep -qE '^[0-9a-f]{64}$' "$rel_file"; then die 3 "refusing: $rel_file is missing or has no digest; cannot prove this is not the release cert"; fi
if grep -qx "$digest" "$rel_file"; then
  die 3 "refusing: this is the RELEASE cert; create a separate upload key (runbook step E)"
fi
old=""
[ -f "$pin_file" ] && old="$(grep -m1 -E '^[0-9a-f]{64}$' "$pin_file" || true)"
if [ "$old" = "$digest" ]; then echo "pin already set to this digest: $digest"; exit 0; fi
if [ -n "$old" ] && [ "$replace" != 1 ]; then
  die 3 "refusing: pin already set (old $old, new $digest); re-run with --replace if this is intended"
fi
tmpf="$(mktemp "$PIN_DIR/.pin.XXXXXX")"
trap 'rm -f "$tmpf"' EXIT
{ [ -f "$pin_file" ] && { grep '^#' "$pin_file" || true; }; echo "$digest"; } >"$tmpf"
chmod 644 "$tmpf"
mv "$tmpf" "$pin_file"
cat <<MSG
upload-cert pin set: $digest
Next:
  1. git switch -c chore/play-upload-pin && git add android/app/play-upload-cert-sha256.txt
  2. git commit -m "play: pin the upload cert" and open a PR; merge it
  3. dry run:  gh workflow run play-publish.yml --ref main -f dry_run=true
  4. real run: gh workflow run play-publish.yml --ref main -f version=X.Y.Z -f dry_run=false
MSG
