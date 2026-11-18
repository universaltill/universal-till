#!/usr/bin/env bash
# Publish the signed Windows installer of a release to the public `installers`
# container, so Microsoft Partner Center gets a stable, DIRECT (HTTP 200, no
# redirect) HTTPS URL (ut-docs#6407, #6405). GitHub release asset URLs 302.
#
#   publish-installer.sh <tag> [expected-sha256]
#
# Env: GH_REPO + GITHUB_TOKEN (gh), STORAGE_ACCOUNT, CONTAINER (default
# installers), optional BASE_URL (tests), SIGN_EXE (default
# packaging/windows/sign-exe.sh), optional GITHUB_OUTPUT.
# Steps, each fatal: download the release asset; its sha256 must equal the
# release digest (and expected-sha256 if given); Authenticode verify; upload
# WITHOUT overwrite (a published file is never replaced; an existing blob
# fails the run); then curl the final URL: HTTP 200, no Location, same
# Content-Length, same sha256 after download (no redirects followed).
# Infra: ut-infra unitill-infra/installers.tf + release-signing/.
set -euo pipefail

tag="${1:-}"
expected="${2:-}"
if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
  echo "::error::publish-installer: tag '$tag' is not vMAJOR.MINOR.PATCH[-pre]" >&2
  exit 2
fi
if [ -n "$expected" ] && ! [[ "$expected" =~ ^[0-9a-f]{64}$ ]]; then
  echo "::error::publish-installer: expected sha256 is not 64 hex chars" >&2
  exit 2
fi
ver="${tag#v}"
container="${CONTAINER:-installers}"
: "${STORAGE_ACCOUNT:?STORAGE_ACCOUNT is required}"
base="${BASE_URL:-https://${STORAGE_ACCOUNT}.blob.core.windows.net/${container}}"
asset="unitill-pos-setup-${ver}.exe"
blob="windows/v${ver}/${asset}"
url="${base}/${blob}"
sign_exe="${SIGN_EXE:-packaging/windows/sign-exe.sh}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

gh release download "$tag" --pattern "$asset" --dir "$work"
f="$work/$asset"
[ -s "$f" ] || { echo "::error::release $tag has no $asset" >&2; exit 1; }
sha="$(sha256sum "$f" | awk '{print $1}')"

digest="$(gh api "repos/${GH_REPO:?GH_REPO is required}/releases/tags/${tag}" \
  --jq ".assets[] | select(.name==\"${asset}\") | .digest")"
digest="${digest#sha256:}"
if [ "$digest" != "$sha" ]; then
  echo "::error::sha256 of the downloaded $asset ($sha) != the release digest ('${digest}')" >&2
  exit 1
fi
if [ -n "$expected" ] && [ "$expected" != "$sha" ]; then
  echo "::error::sha256 ($sha) != the expected sha256 from the build ($expected)" >&2
  exit 1
fi

"$sign_exe" --verify-only "$f"

# --overwrite false is the default stated explicitly; never true. An existing
# blob makes az fail (append-only). Re-run safety: if the upload failed because
# the blob already exists, the run only succeeds when the PUBLIC bytes are
# identical (checked below); different bytes at that URL still fail.
if ! az storage blob upload --auth-mode login --account-name "$STORAGE_ACCOUNT" \
  --container-name "$container" --name "$blob" --file "$f" \
  --content-type application/vnd.microsoft.portable-executable --overwrite false --only-show-errors 2>"$work/az.err"; then
  if grep -q 'BlobAlreadyExists' "$work/az.err"; then
    echo "blob exists already; verifying the public bytes are identical" >&2
  else
    cat "$work/az.err" >&2; exit 1
  fi
fi

size="$(wc -c <"$f" | tr -d ' ')"
head="$(curl -sI --max-time 60 "$url")"
status="$(printf '%s\n' "$head" | sed -n 1p | tr -d '\r' | awk '{print $2}')"
if [ "$status" != "200" ]; then
  echo "::error::$url answered HTTP '$status', want 200" >&2; exit 1
fi
if printf '%s\n' "$head" | grep -i '^location:' >/dev/null; then
  echo "::error::$url redirects (Location header present)" >&2; exit 1
fi
clen="$(printf '%s\n' "$head" | tr -d '\r' | awk 'tolower($1)=="content-length:"{print $2}')"
if [ "$clen" != "$size" ]; then
  echo "::error::$url Content-Length '$clen' != $size" >&2; exit 1
fi
curl -fsS --max-time 300 -o "$work/check.exe" "$url"
got="$(sha256sum "$work/check.exe" | awk '{print $1}')"
if [ "$got" != "$sha" ]; then
  echo "::error::$url serves sha256 $got, want $sha" >&2; exit 1
fi

echo "published: $url (sha256 $sha)"
if [ -n "${GITHUB_OUTPUT:-}" ]; then echo "url=$url" >> "$GITHUB_OUTPUT"; fi
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then echo "Installer URL: \`$url\` (sha256 \`$sha\`)" >> "$GITHUB_STEP_SUMMARY"; fi
