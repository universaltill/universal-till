#!/usr/bin/env bash
#
# Submit the published release's versioned installer URL to the Microsoft
# Store (ut-docs#6407). Runs only in the `msstore-submit` environment of
# msstore-submit.yml, after msstore-verify.sh said ready_to_submit.
#
#   msstore-submit.sh <tag>
#
# Env: MSSTORE_APP_ID (Store product id), MSSTORE_TOKEN_FILE (a 0600 file with
# the Entra access token, written by the workflow; never an argument, never
# printed), COMMIT=true to commit the submission (default: leave it as a draft
# in Partner Center), REPO (default universaltill/universal-till).
#
# Fails closed:
#   - until store-release/microsoft.opened exists and names its owner-approved
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
fail() { echo "::error::msstore-submit: $1" >&2; exit 1; }

opened="${ROOT_DIR}/store-release/microsoft.opened"
[ -f "$opened" ] || fail "the Microsoft Store is not opened: store-release/microsoft.opened is missing (the owner's opening card ut-docs#6408 adds it)"
grep -qE 'ut-docs#[0-9]+' "$opened" || fail "store-release/microsoft.opened must name its owner-approved card (ut-docs#N)"

[[ "$tag_in" =~ ^v?([0-9]+\.[0-9]+\.[0-9]+)$ ]] || fail "tag '${tag_in}' is not vX.Y.Z"
version="${BASH_REMATCH[1]}"
tag="v${version}"
want_url="https://github.com/${REPO}/releases/download/${tag}/unitill-pos-setup-${version}.exe"
url="${2:-$want_url}"
[ "$url" = "$want_url" ] || fail "package URL must be exactly ${want_url}"

app="${MSSTORE_APP_ID:-}"
[[ "$app" =~ ^[A-Za-z0-9]{6,32}$ ]] || fail "MSSTORE_APP_ID is missing or malformed"
tokfile="${MSSTORE_TOKEN_FILE:-}"
[ -n "$tokfile" ] && [ -s "$tokfile" ] || fail "MSSTORE_TOKEN_FILE is missing or empty"

state="$(gh release view "$tag" --repo "$REPO" --json isDraft,isPrerelease --jq '"\(.isDraft) \(.isPrerelease)"')" \
  || fail "cannot read release ${tag}"
[ "$state" = "false false" ] || fail "release ${tag} is not published (draft/prerelease: ${state})"

ep() { # ep KEY -> value from the endpoints data file (not sourced: data only)
  local v
  v="$(sed -n "s/^$1=//p" "${ROOT_DIR}/scripts/ci/msstore-submit.endpoints" | sed -n 1p)" || true
  [ -n "$v" ] || fail "endpoint $1 missing"
  printf '%s' "$v"
}
base="$(ep API_BASE)"
[[ "$base" =~ ^https://[a-z0-9.-]+(/[A-Za-z0-9._/-]*)?$ ]] || fail "API_BASE must be an https URL"

tmp="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${tmp:?}"; }
trap cleanup EXIT
# The token reaches curl through a header file, never an argument.
( umask 077; printf 'Authorization: Bearer %s\n' "$(tr -d '\r\n' <"$tokfile")" >"$tmp/hdr" )

# api <METHOD> <path> [json-body-file] -> response body in $tmp/resp
api() {
  local code args=(-sS --proto '=https' --proto-redir '=https' -H "@$tmp/hdr" -H 'Accept: application/json' -X "$1" -o "$tmp/resp" -w '%{http_code}')
  [ -n "${3:-}" ] && args+=(-H 'Content-Type: application/json' --data-binary "@$3")
  code="$(curl "${args[@]}" "${base}$2")" || fail "$1 $2: request failed"
  [[ "$code" =~ ^2[0-9][0-9]$ ]] || fail "$1 $2 answered HTTP ${code}"
}
path() { local p; p="$(ep "$1")"; p="${p//\{app\}/$app}"; printf '%s' "${p//\{sub\}/${2:-}}"; }

api POST "$(path CREATE_PATH)"
sub="$(jq -r '.id // empty' "$tmp/resp")"
[[ "$sub" =~ ^[A-Za-z0-9-]{1,64}$ ]] || fail "the create call returned no usable submission id"

jq -n --arg url "$url" '{applicationPackages:[{packageUrl:$url,architecture:"X64",installerParameters:"/S",isSilentInstall:true,languages:["en-GB"]}]}' >"$tmp/body.json"
api PUT "$(path UPDATE_PATH "$sub")" "$tmp/body.json"
echo "package set on submission ${sub}: ${url} (x64, /S, en-GB)"

if [ "${COMMIT:-false}" = true ]; then
  api POST "$(path COMMIT_PATH "$sub")"
  echo "submission ${sub} committed"
else
  echo "submission ${sub} left uncommitted (COMMIT is not true)"
fi
