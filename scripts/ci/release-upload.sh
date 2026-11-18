#!/usr/bin/env bash
#
# Upload release assets without ever overwriting a PUBLISHED one (ut-docs#6405).
#
# A published asset is what tills self-update from and what the Microsoft
# Store submission points at by versioned URL: its bytes must never change
# after publication. A bare `gh release upload --clobber` would replace them
# silently, so release.yml uploads through this script instead.
#
#   release-upload.sh <tag> <file>...
#
#   draft release       -> upload with --clobber (a retried job whose first
#                          attempt half-succeeded must not hit the 422 of a
#                          double upload; nothing is public yet)
#   published release  -> upload only assets that do not exist yet (the
#                          macOS .dmg, attached after publish); an asset
#                          that already exists is REFUSED, except the two
#                          checksum files, which macos-dmg-attach must
#                          extend with the .dmg line (ADR-0178):
#                          checksums.txt and checksums.txt.sig. "Extend"
#                          means the published lines are kept as a byte for
#                          byte prefix of the new file, in order, and only
#                          lines are appended; in checksums.txt every
#                          appended line is '<sha256>  <file>' and names an
#                          asset the published file does not list (no second
#                          hash for a published asset, ut-docs#6636).
#
# Fails closed: if the release state or its asset list cannot be read, nothing
# is uploaded.
set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <tag> <file>..." >&2
  exit 2
fi
tag=$1
shift

pub_dir="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${pub_dir:?}"; }
trap cleanup EXIT

# extend_only <tag> <name> <new-file>: refuse unless <new-file> is the
# published <name> plus appended lines.
extend_only() {
  local tag=$1 name=$2 new=$3 pub bad n line asset
  pub="${pub_dir}/${name}"
  if ! gh release download "$tag" --pattern "$name" --dir "$pub_dir" >/dev/null || [ ! -f "$pub" ]; then
    echo "::error::release-upload: cannot download the published ${name} of ${tag}; refusing to replace it (ut-docs#6636)" >&2
    exit 1
  fi
  bad="$(awk 'NR==FNR{p[++k]=$0; next} FNR<=k && $0!=p[FNR]{print FNR; b=1; exit} END{if(!b && FNR<k) print FNR+1}' "$pub" "$new")" || bad=1
  if [ -n "$bad" ]; then
    echo "::error::release-upload: ${name} of published ${tag} may only be extended: published line ${bad} is changed or missing (ut-docs#6636)" >&2
    exit 1
  fi
  if [ "$name" = checksums.txt ]; then
    local k seen
    k="$(awk 'END{print NR}' "$pub")"
    seen="$(awk '{print $2}' "$pub")"
    n=0
    while IFS= read -r line || [ -n "$line" ]; do
      n=$((n + 1))
      [ "$n" -le "$k" ] && continue
      if ! [[ "$line" =~ ^[0-9a-f]{64}\ \ [^\ ]+$ ]]; then
        echo "::error::release-upload: appended line $((n - k)) of ${name} is not '<sha256>  <file>' (ut-docs#6636)" >&2
        exit 1
      fi
      asset="${line:66}"
      if grep -qxF -- "$asset" <<<"$seen"; then
        echo "::error::release-upload: ${name} of published ${tag} already lists ${asset}; a second hash for a published asset is refused (ut-docs#6636)" >&2
        exit 1
      fi
      seen="${seen}"$'\n'"${asset}"
    done <"$new"
  fi
}

state="$(gh release view "$tag" --json isDraft --jq '.isDraft')" \
  || { echo "::error::release-upload: cannot read release ${tag}" >&2; exit 1; }
case "$state" in
  true | false) ;;
  *) echo "::error::release-upload: unexpected isDraft value '${state}' for ${tag}" >&2; exit 1 ;;
esac

existing=""
if [ "$state" = false ]; then
  existing="$(gh release view "$tag" --json assets --jq '.assets[].name')" \
    || { echo "::error::release-upload: cannot list assets of ${tag}" >&2; exit 1; }
fi

for f in "$@"; do
  [ -f "$f" ] || { echo "::error::release-upload: no such file: $f" >&2; exit 1; }
  name="$(basename "$f")"
  if [ "$state" = true ]; then
    # Draft: replacing is retry-safe.
    gh release upload "$tag" "$f" --clobber # release-asset-clobber:allow draft-only
    continue
  fi
  if grep -qxF -- "$name" <<<"$existing"; then
    case "$name" in
      checksums.txt | checksums.txt.sig) ;;
      *)
        echo "::error::release-upload: ${tag} is published and already has ${name}; a published asset is never overwritten (ut-docs#6405)" >&2
        exit 1
        ;;
    esac
    # Published checksum files: the one reviewed replacement, extend-only.
    extend_only "$tag" "$name" "$f"
    gh release upload "$tag" "$f" --clobber # release-asset-clobber:allow checksums-only
  else
    gh release upload "$tag" "$f"
  fi
done
