#!/usr/bin/env bash
#
# Upload one signed Android App Bundle to the Google Play INTERNAL testing
# track through the Play Developer API (Edits). ut-docs#6410, ADR-0194 s7.
#
# Internal only, by construction: the track is spelled out below and there is
# no argument, env var or input that changes it. A public/production rollout
# is a separate, owner-gated job added by the card that opens Google Play
# (#6411) together with its `store-release:allow` marker.
#
# Idempotent and never replaces bytes (ut-docs#6405): if Play already holds a
# bundle with the same versionCode, the run is a no-op when its sha256 equals
# ours and FAILS when it differs. A re-run therefore cannot silently swap an
# already-published binary.
#
# Input (env):
#   PLAY_ACCESS_TOKEN   short-lived OAuth token, androidpublisher scope only
#                       (google-github-actions/auth, token_format access_token)
#   PLAY_PACKAGE_NAME   application id
#   AAB                 path of the signed bundle
#   VERSION_CODE        expected versionCode (base*10+5); Play's answer must match
#   VERSION_NAME        release name shown in the console
# Dry-run of the logic only: PLAY_API_BASE may point at a local fake server
# (scripts/ci/play-upload_test.sh); it must be https:// or http://127.0.0.1.
set -euo pipefail

API="${PLAY_API_BASE:-https://androidpublisher.googleapis.com}"
case "$API" in
  https://* | http://127.0.0.1:*) ;;
  *) echo "::error::PLAY_API_BASE must be https:// (or a 127.0.0.1 test server)" >&2; exit 1 ;;
esac

: "${PLAY_ACCESS_TOKEN:?}" "${PLAY_PACKAGE_NAME:?}" "${AAB:?}" "${VERSION_CODE:?}" "${VERSION_NAME:?}"
[[ "$VERSION_CODE" =~ ^[1-9][0-9]*$ ]] || { echo "::error::VERSION_CODE '$VERSION_CODE' is not a positive integer" >&2; exit 1; }
[[ "$PLAY_PACKAGE_NAME" =~ ^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$ ]] || { echo "::error::bad PLAY_PACKAGE_NAME" >&2; exit 1; }
[ -f "$AAB" ] || { echo "::error::$AAB not found" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
umask 077
# The token goes to curl through a header file, never an argv (ps) or a log.
printf 'Authorization: Bearer %s\n' "$PLAY_ACCESS_TOKEN" >"$work/hdr"
call() { curl --fail-with-body --silent --show-error --proto '=https,http' --max-time 600 -H "@$work/hdr" "$@"; }

base="$API/androidpublisher/v3/applications/$PLAY_PACKAGE_NAME"
local_sha="$(shasum -a 256 "$AAB" | cut -d' ' -f1)"

edit="$(call -X POST -H 'Content-Type: application/json' -d '{}' "$base/edits" | jq -er .id)"

existing="$(call "$base/edits/$edit/bundles" | jq -c --argjson v "$VERSION_CODE" '[(.bundles // [])[] | select(.versionCode == $v)] | first // empty')"
if [ -n "$existing" ]; then
  remote_sha="$(jq -r '.sha256 // ""' <<<"$existing")"
  call -X DELETE "$base/edits/$edit" >/dev/null || true
  if [ "$remote_sha" = "$local_sha" ]; then
    echo "versionCode $VERSION_CODE is already on Play with the same sha256; nothing to do."
    exit 0
  fi
  echo "::error::versionCode $VERSION_CODE already exists on Play with a DIFFERENT sha256 (play=$remote_sha local=$local_sha). Refusing to replace a published asset (ut-docs#6405); bump the version." >&2
  exit 1
fi

got="$(call -X POST -H 'Content-Type: application/octet-stream' --data-binary "@$AAB" \
  "$API/upload/androidpublisher/v3/applications/$PLAY_PACKAGE_NAME/edits/$edit/bundles?uploadType=media" | jq -er .versionCode)"
if [ "$got" != "$VERSION_CODE" ]; then
  call -X DELETE "$base/edits/$edit" >/dev/null || true
  echo "::error::Play read versionCode $got from the bundle, expected $VERSION_CODE; nothing committed." >&2
  exit 1
fi

# The track is spelled out (not a variable) so guard-store-private.sh can see it.
jq -n --arg n "$VERSION_NAME" --arg c "$VERSION_CODE" \
  '{track:"internal", releases:[{name:$n, status:"completed", versionCodes:[$c]}]}' |
  call -X PUT -H 'Content-Type: application/json' -d @- "$base/edits/$edit/tracks/internal" >/dev/null
call -X POST "$base/edits/$edit:commit" >/dev/null
echo "Uploaded $VERSION_NAME (versionCode $VERSION_CODE) to the internal track."
