#!/usr/bin/env bash
#
# Pre-submission verification of the release the Microsoft Store will fetch
# (ut-docs#6407). Holds no secret. Writes <outdir>/result.json and
# <outdir>/summary.md (also to $GITHUB_STEP_SUMMARY) and exits non-zero unless
# every check passed, so `ready_to_submit` is true only for a verified release.
#
#   msstore-verify.sh <tag> <outdir>
#
# Checks, in order, each failing closed:
#   1. tag shape (vX.Y.Z) and the release: published, not draft, not prerelease
#   2. the versioned asset URL (never /latest/) and its asset in the release
#   3. download into <outdir> (fresh and empty); sha256 == the GitHub asset
#      `digest` == the signed checksums.txt line (ADR-0178: the signature must
#      verify against UT_RELEASE_KEYS_HEX, which must be configured)
#   4. Authenticode signature + timestamp (packaging/windows/sign-exe.sh
#      --verify-only) and signer TASK RUNNER TECHNOLOGY LTD
#   5. the installer supports a silent install (/S) -- the installer.nsi of
#      the tag, via check-installer-silent.sh
#   6. soak: the URL serves the same bytes over a window (msstore-soak.sh)
#
# Env: REPO (default universaltill/universal-till), SIGN_PUBLISHER and
# SIGN_CA_BUNDLE (sign-exe.sh), UT_RELEASE_KEYS_HEX, SOAK_CHECKS (3),
# SOAK_INTERVAL seconds (60). Overrides for tests: SIGN_EXE, SOAK_CMD,
# SILENT_CMD, SIG_VERIFY_CMD, INSTALLER_NSI.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tag_in=${1:?usage: msstore-verify.sh <tag> <outdir>}
out=${2:?usage: msstore-verify.sh <tag> <outdir>}
REPO="${REPO:-universaltill/universal-till}"
EXPECT_SIGNER="TASK RUNNER TECHNOLOGY LTD"
SIGN_EXE="${SIGN_EXE:-${ROOT_DIR}/packaging/windows/sign-exe.sh}"
SOAK_CMD="${SOAK_CMD:-${ROOT_DIR}/scripts/ci/msstore-soak.sh}"
SILENT_CMD="${SILENT_CMD:-${ROOT_DIR}/scripts/ci/check-installer-silent.sh}"

url="" sha="" size="" signer="" silent=false verdict=false reason="not finished"
write_result() {
  mkdir -p "$out"
  jq -n --arg tag "${tag:-}" --arg url "$url" --arg sha "$sha" --arg size "$size" --arg signer "$signer" \
    --argjson silent "$silent" --argjson verdict "$verdict" --arg reason "$reason" \
    '{tag:$tag,url:$url,sha256:$sha,size:$size,signer:$signer,silent_install_flag:"/S",silent_install_supported:$silent,ready_to_submit:$verdict,reason:$reason}' \
    >"$out/result.json"
  {
    echo "### Microsoft Store pre-submission check: ${tag:-$tag_in}"
    echo "- URL: ${url:-n/a}"
    echo "- sha256: ${sha:-n/a}"
    echo "- size: ${size:-n/a}"
    echo "- signer: ${signer:-n/a}"
    echo "- silent install flag: /S (supported: ${silent})"
    echo "- ready_to_submit: **${verdict}** (${reason})"
  } >"$out/summary.md"
  [ -n "${GITHUB_STEP_SUMMARY:-}" ] && cat "$out/summary.md" >>"$GITHUB_STEP_SUMMARY"
  return 0
}
die() { reason="$1"; echo "::error::msstore-verify: $1" >&2; write_result; exit 1; }
tag=""

[[ "$tag_in" =~ ^v?([0-9]+\.[0-9]+\.[0-9]+)$ ]] || die "tag '${tag_in}' is not vX.Y.Z"
version="${BASH_REMATCH[1]}"
tag="v${version}"

if [ -e "$out" ] && [ -n "$(ls -A "$out" 2>/dev/null)" ]; then
  die "output dir ${out} is not empty"
fi
mkdir -p "$out/dl"

meta="$(gh release view "$tag" --repo "$REPO" --json isDraft,isPrerelease,assets)" || die "release ${tag} not found"
[ "$(jq -r '.isDraft' <<<"$meta")" = false ] || die "release ${tag} is a draft"
[ "$(jq -r '.isPrerelease' <<<"$meta")" = false ] || die "release ${tag} is a prerelease"

name="unitill-pos-setup-${version}.exe"
url="https://github.com/${REPO}/releases/download/${tag}/${name}"
asset="$(jq -c --arg n "$name" '[.assets[] | select(.name==$n)][0] // empty' <<<"$meta")"
[ -n "$asset" ] || die "release ${tag} has no asset ${name}"
api_digest="$(jq -r '.digest // empty' <<<"$asset")"
api_size="$(jq -r '.size' <<<"$asset")"
[[ "$api_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "the GitHub asset digest for ${name} is missing or not sha256"

exe="$out/dl/${name}"
curl -fsSL --proto '=https' --proto-redir '=https' -o "$exe" "$url" || die "download of ${url} failed"
sha="$(shasum -a 256 "$exe" | awk '{print $1}')"
size="$(wc -c <"$exe" | tr -d ' ')"
[ "sha256:${sha}" = "$api_digest" ] || die "downloaded sha256 ${sha} != GitHub asset digest ${api_digest}"
[ "$size" = "$api_size" ] || die "downloaded ${size} bytes != GitHub asset size ${api_size}"

gh release download "$tag" --repo "$REPO" --pattern checksums.txt --dir "$out/dl" || die "release has no checksums.txt"
line="$(grep -E "^[0-9a-f]{64}  ${name//./\\.}\$" "$out/dl/checksums.txt" || true)"
[ "$(wc -l <<<"$line" | tr -d ' ')" = 1 ] && [ -n "$line" ] || die "checksums.txt has no single line for ${name}"
[ "${line%% *}" = "$sha" ] || die "checksums.txt sha256 ${line%% *} != downloaded ${sha}"
[ -n "${UT_RELEASE_KEYS_HEX:-}" ] || die "release signing keys (UT_RELEASE_KEYS_HEX) are not configured: cannot verify checksums.txt.sig (ADR-0178)"
gh release download "$tag" --repo "$REPO" --pattern checksums.txt.sig --dir "$out/dl" || die "release has no checksums.txt.sig"
if [ -n "${SIG_VERIFY_CMD:-}" ]; then
  $SIG_VERIFY_CMD "$out/dl/checksums.txt" "$out/dl/checksums.txt.sig" || die "checksums.txt.sig does not verify"
else
  (cd "$ROOT_DIR" && go run ./scripts/releasesig verify --checksums "$out/dl/checksums.txt" --sig "$out/dl/checksums.txt.sig") \
    || die "checksums.txt.sig does not verify"
fi

[ "${SIGN_PUBLISHER:-}" = "$EXPECT_SIGNER" ] || die "SIGN_PUBLISHER must be '${EXPECT_SIGNER}'"
"$SIGN_EXE" --verify-only "$exe" || die "Authenticode verification failed or signer is not ${EXPECT_SIGNER}"
signer="$EXPECT_SIGNER"

nsi="${INSTALLER_NSI:-}"
if [ -z "$nsi" ]; then
  nsi="$out/installer.nsi"
  (cd "$ROOT_DIR" && git show "${tag}:packaging/windows/installer.nsi") >"$nsi" || die "cannot read installer.nsi at ${tag}"
fi
"$SILENT_CMD" "$nsi" || die "installer does not support a silent install (/S)"
silent=true

"$SOAK_CMD" "$url" "$sha" "$size" "${SOAK_CHECKS:-3}" "${SOAK_INTERVAL:-60}" || die "soak check failed"

verdict=true
reason="all checks passed"
write_result
echo "ready_to_submit=true for ${url}"
