#!/usr/bin/env bash
#
# Submit the published release's versioned installer URL to the Microsoft
# Store (ut-docs#6407). Runs only in the `msstore-submit` environment of
# msstore-submit.yml, after msstore-verify.sh said ready_to_submit.
#
#   msstore-submit.sh <tag>
#
# Env: MSSTORE_APP_ID (Store product id), MSSTORE_SELLER_ID (Partner Center
# seller id, sent as X-Seller-Account-Id), DRY_RUN=true (READ-ONLY probe: only
# GET packages and GET status, proves token, seller id and product id without
# changing anything; needs no opening file), EXPECT_SHA256 (the digest the
# verify job proved; re-checked against the release asset before any write),
# MSSTORE_GENERIC_DOC_URL (required for an EXE package, default the website),
# MSSTORE_TOKEN_FILE (a 0600 file with
# the Entra access token, written by the workflow; never an argument, never
# printed), COMMIT=true to commit the submission (default: leave it as a draft
# in Partner Center), REPO (default universaltill/universal-till).
#
# Fails closed:
#   - (writes only) until store-release/microsoft.opened exists and names its owner-approved
#     card (ut-docs#N): the owner's opening PR adds it (ut-docs#6408), together
#     with the store-release:allow marker the store guard demands;
#   - on a tag that is not vX.Y.Z, a release that is not published (not draft,
#     not prerelease), or a package URL that is not this repo's versioned
#     release-asset URL (never /latest/, never another host or repo);
#   - on ANY non-2xx API answer (the token is never echoed, the body is not
#     printed beyond the status).
set -euo pipefail

ROOT_DIR="${MSSTORE_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
tag_in=${1:?usage: msstore-submit.sh <tag> [package-url]}
REPO="${REPO:-universaltill/universal-till}"
DRY_RUN="${DRY_RUN:-true}"
fail() { echo "::error::msstore-submit: $1" >&2; exit 1; }
[ "$DRY_RUN" = true ] || [ "$DRY_RUN" = false ] || fail "DRY_RUN must be true or false"

if [ "$DRY_RUN" = false ]; then
  opened="${ROOT_DIR}/store-release/microsoft.opened"
  [ -f "$opened" ] || fail "the Microsoft Store is not opened: store-release/microsoft.opened is missing (the owner's opening card ut-docs#6408 adds it)"
  grep -qE 'ut-docs#[0-9]+' "$opened" || fail "store-release/microsoft.opened must name its owner-approved card (ut-docs#N)"
fi

[[ "$tag_in" =~ ^v?([0-9]+\.[0-9]+\.[0-9]+)$ ]] || fail "tag '${tag_in}' is not vX.Y.Z"
version="${BASH_REMATCH[1]}"
tag="v${version}"
name="unitill-pos-setup-${version}.exe"
want_url="https://github.com/${REPO}/releases/download/${tag}/${name}"
url="${2:-$want_url}"
[ "$url" = "$want_url" ] || fail "package URL must be exactly ${want_url}"

app="${MSSTORE_APP_ID:-}"
[[ "$app" =~ ^[A-Za-z0-9]{6,32}$ ]] || fail "MSSTORE_APP_ID is missing or malformed"
seller="${MSSTORE_SELLER_ID:-}"
[[ "$seller" =~ ^[0-9]{4,20}$ ]] || fail "MSSTORE_SELLER_ID is missing or malformed"
docurl="${MSSTORE_GENERIC_DOC_URL:-https://universaltill.com/}"
[[ "$docurl" =~ ^https://[A-Za-z0-9./_-]+$ ]] || fail "MSSTORE_GENERIC_DOC_URL must be an https URL"
tokfile="${MSSTORE_TOKEN_FILE:-}"
[ -n "$tokfile" ] && [ -s "$tokfile" ] || fail "MSSTORE_TOKEN_FILE is missing or empty"

state="$(gh release view "$tag" --repo "$REPO" --json isDraft,isPrerelease --jq '"\(.isDraft) \(.isPrerelease)"')" \
  || fail "cannot read release ${tag}"
[ "$state" = "false false" ] || fail "release ${tag} is not published (draft/prerelease: ${state})"

if [ "$DRY_RUN" = false ]; then
  # The asset must still be the bytes verify proved, however long the approval waited.
  expect="${EXPECT_SHA256:-}"
  [[ "$expect" =~ ^[0-9a-f]{64}$ ]] || fail "EXPECT_SHA256 (the verified digest) is missing or malformed"
  now="$(gh release view "$tag" --repo "$REPO" --json assets --jq ".assets[] | select(.name==\"${name}\") | .digest")" \
    || fail "cannot read the asset digest"
  [ "$now" = "sha256:${expect}" ] || fail "the release asset changed since verification (digest ${now:-none}, verified sha256:${expect})"
fi

ep() { # ep KEY -> value from the endpoints data file (not sourced: data only)
  local v
  v="$(sed -n "s/^$1=//p" "${ROOT_DIR}/scripts/ci/msstore-submit.endpoints" | sed -n 1p)" || true
  [ -n "$v" ] || fail "endpoint $1 missing"
  printf '%s' "$v"
}
base="$(ep API_BASE)"
[[ "$base" =~ ^https://[a-z0-9.-]+$ ]] || fail "API_BASE must be an https origin"

tmp="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${tmp:?}"; }
trap cleanup EXIT
# The token reaches curl through a header file, never an argument.
( umask 077; printf 'Authorization: Bearer %s\n' "$(tr -d '\r\n' <"$tokfile")" >"$tmp/hdr" )

# api <METHOD> <path> [json-body-file] -> response body in $tmp/resp
api() {
  local code args=(-sS --proto '=https' --proto-redir '=https' -H "@$tmp/hdr" -H "X-Seller-Account-Id: ${seller}" -H 'Accept: application/json' -X "$1" -o "$tmp/resp" -w '%{http_code}')
  [ -n "${3:-}" ] && args+=(-H 'Content-Type: application/json' --data-binary "@$3")
  code="$(curl "${args[@]}" "${base}$2")" || fail "$1 $2: request failed"
  [[ "$code" =~ ^2[0-9][0-9]$ ]] || fail "$1 $2 answered HTTP ${code}"
}
path() { local p; p="$(ep "$1")"; printf '%s' "${p//\{id\}/$app}"; }

# Read-only probe: proves the token, seller id and product id.
api GET "$(path PACKAGES_PATH)"
echo "probe ok: packages readable for product ${app}"
api GET "$(path STATUS_PATH)"
echo "probe ok: status readable"
if [ "$DRY_RUN" = true ]; then
  echo "dry run: nothing was changed"
  exit 0
fi

jq -n --arg url "$url" --arg doc "$docurl" \
  '{packages:[{packageUrl:$url,languages:["en-gb"],architectures:["X64"],isSilentInstall:false,installerParameters:"/S",genericDocUrl:$doc,packageType:"exe"}]}' >"$tmp/body.json"
api PUT "$(path PACKAGES_PATH)" "$tmp/body.json"
echo "package set: ${url} (x64, exe, /S, en-gb)"

if [ "${COMMIT:-false}" = true ]; then
  api POST "$(path COMMIT_PATH)"
  echo "package changes committed; polling status"
  ready=""
  for _ in $(seq 1 30); do
    api GET "$(path STATUS_PATH)"
    if jq -e '.responseData.isReady == true' "$tmp/resp" >/dev/null 2>&1; then ready=1; break; fi
    if jq -e '[.responseData.errors[]?.code] | any(. == "packageuploaderror")' "$tmp/resp" >/dev/null 2>&1; then fail "the Store reported a package upload error"; fi
    sleep "${MSSTORE_POLL_SECONDS:-20}"
  done
  [ -n "$ready" ] || fail "the Store did not report the package ready in time"
  api POST "$(path SUBMIT_PATH)"
  echo "submission sent to certification"
else
  echo "package set, not committed (COMMIT is not true): finish in Partner Center or re-run with commit"
fi
