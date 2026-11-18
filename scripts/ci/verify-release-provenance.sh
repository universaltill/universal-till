#!/usr/bin/env bash
#
# Verifies the build-provenance attestations of every file in a directory of
# release assets (ADR-0181, ut-docs#5437). Run by release.yml's `provenance`
# (pre-publish) and `provenance-dmg` (post-publish) jobs right after they
# attest, and usable by hand against a downloaded release.
#
# Usage: verify-release-provenance.sh <assets-dir> <tag> [<source-digest>]
#   assets-dir      directory holding the files to verify (regular files,
#                   not recursive)
#   tag             release tag, e.g. v1.2.3
#   source-digest   commit the tag points at; default: git rev-parse TAG^{commit}
#
# Needs: gh (with `attestation verify`), jq, sha256sum, and GITHUB_REPOSITORY
# (owner/name, set by Actions; set it by hand elsewhere). GH_TOKEN is only
# needed if gh asks for one.
#
# For each file it requires a valid attestation that is:
#   - for this repository, signed by this repository's release.yml workflow,
#   - for the tag's commit (--source-digest),
#   - from a GitHub-hosted runner (--deny-self-hosted-runners),
#   - from ref refs/tags/<tag> (a tag push) or refs/heads/main (the one-click
#     dispatch, which tags the commit itself). No single source-ref flag: it
#     would break one of the two paths.
# Then a negative drill: verification MUST fail for a byte-modified copy and
# for a wrong --signer-workflow. A check that cannot fail proves nothing.
#
# This proves which commit and workflow built a file. It does not prove the
# commit is benign (ADR-0181 section 3).
set -euo pipefail

DIR="${1:?usage: verify-release-provenance.sh <assets-dir> <tag> [<source-digest>]}"
TAG="${2:?usage: verify-release-provenance.sh <assets-dir> <tag> [<source-digest>]}"
REPO="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY must be set (owner/name)}"
SHA="${3:-}"
if [ -z "$SHA" ]; then
  SHA="$(git rev-parse "${TAG}^{commit}")"
fi
SIGNER="${REPO}/.github/workflows/release.yml"

[ -d "$DIR" ] || { echo "::error::assets directory not found: $DIR" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

verify() { # verify <file> <signer-workflow>; JSON result on stdout
  gh attestation verify "$1" \
    --repo "$REPO" \
    --signer-workflow "$2" \
    --source-digest "$SHA" \
    --deny-self-hosted-runners \
    --format json
}

# The positive per-asset verify gets a bounded retry: the attestation API can
# lag a few seconds behind the attest step. Still fail-closed (every attempt
# must fail for the asset to fail); the negative drills below never retry.
VERIFY_ATTEMPTS="${VERIFY_ATTEMPTS:-3}"
VERIFY_SLEEP="${VERIFY_SLEEP:-5}"
verify_with_retry() { # verify_with_retry <file> <signer-workflow>
  local n=1
  while :; do
    if verify "$1" "$2"; then return 0; fi
    if [ "$n" -ge "$VERIFY_ATTEMPTS" ]; then return 1; fi
    echo "verify attempt ${n}/${VERIFY_ATTEMPTS} failed for $(basename "$1"); retrying in ${VERIFY_SLEEP}s" >&2
    n=$((n + 1))
    sleep "$VERIFY_SLEEP"
  done
}

fails=0
count=0
first=""
while IFS= read -r f; do
  count=$((count + 1))
  [ -n "$first" ] || first="$f"
  name="$(basename "$f")"
  if ! verify_with_retry "$f" "$SIGNER" >"$work/v.json" 2>"$work/v.err"; then
    echo "::error::no valid attestation for $name (repo ${REPO}, workflow release.yml, commit ${SHA})" >&2
    sed 's/^/    /' "$work/v.err" >&2 || true
    fails=$((fails + 1))
    continue
  fi
  refs="$(jq -r '.[]? | .verificationResult.signature.certificate.sourceRepositoryRef // empty' "$work/v.json")"
  ok=0
  while IFS= read -r ref; do
    case "$ref" in
      "refs/tags/${TAG}" | refs/heads/main) ok=1 ;;
    esac
  done <<<"$refs"
  if [ "$ok" -ne 1 ]; then
    echo "::error::$name is attested, but not from refs/tags/${TAG} or refs/heads/main (got: ${refs:-none})" >&2
    fails=$((fails + 1))
    continue
  fi
  echo "ok   - $name attested by release.yml at ${SHA} (${refs//$'\n'/ })"
done < <(find "$DIR" -maxdepth 1 -type f | LC_ALL=C sort)

if [ "$count" -eq 0 ]; then
  echo "::error::no files in $DIR to verify" >&2
  exit 1
fi
if [ "$fails" -ne 0 ]; then
  echo "::error::${fails} of ${count} file(s) lack a valid build-provenance attestation" >&2
  exit 1
fi

# Negative drill (ADR-0181 section 5). Prefer checksums.txt, else the first file.
target="$DIR/checksums.txt"
[ -f "$target" ] || target="$first"

cp "$target" "$work/tampered"
printf 'x' >>"$work/tampered"
if verify "$work/tampered" "$SIGNER" >/dev/null 2>&1; then
  echo "::error::drill failed: a byte-modified copy of $(basename "$target") passed attestation verification" >&2
  exit 1
fi
echo "ok   - drill: a byte-modified copy fails verification"

if verify "$target" "${REPO}/.github/workflows/nightly.yml" >/dev/null 2>&1; then
  echo "::error::drill failed: $(basename "$target") passed verification against a wrong --signer-workflow" >&2
  exit 1
fi
echo "ok   - drill: a wrong --signer-workflow fails verification"
echo "all ${count} file(s) verified"
