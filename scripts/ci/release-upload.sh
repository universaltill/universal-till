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
#                          checksums.txt and checksums.txt.sig.
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
    # Published checksum files: the one reviewed replacement.
    gh release upload "$tag" "$f" --clobber # release-asset-clobber:allow checksums-only
  else
    gh release upload "$tag" "$f"
  fi
done
