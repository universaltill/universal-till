#!/usr/bin/env bash
# Cut a release from the CLI: bump the version, tag, and push — the "Release"
# GitHub workflow then builds and publishes every platform (archives, .deb,
# Windows installer, macOS .dmg) to the GitHub release.
#
# Usage:
#   scripts/release.sh            # patch bump (default)
#   scripts/release.sh minor      # or: major | patch
#   scripts/release.sh 0.3.0      # explicit version
#
# (Or do it entirely in the browser: Actions → "Release" → Run workflow.)
set -euo pipefail
cd "$(dirname "$0")/.."

[ -z "$(git status --porcelain)" ] || { echo "working tree not clean — commit or stash first"; exit 1; }

git fetch --tags --quiet
LATEST="$(git tag -l 'v*' | sort -V | tail -1)"; LATEST="${LATEST#v}"; LATEST="${LATEST:-0.0.0}"

ARG="${1:-patch}"
if [[ "$ARG" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  VERSION="$ARG"
else
  IFS=. read -r MA MI PA <<< "$LATEST"
  case "$ARG" in
    major) MA=$((MA + 1)); MI=0; PA=0 ;;
    minor) MI=$((MI + 1)); PA=0 ;;
    patch) PA=$((PA + 1)) ;;
    *) echo "usage: scripts/release.sh [patch|minor|major|X.Y.Z]"; exit 1 ;;
  esac
  VERSION="${MA}.${MI}.${PA}"
fi
TAG="v${VERSION}"

if git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null; then
  echo "tag ${TAG} already exists"; exit 1
fi

echo "Releasing ${TAG}  (previous: v${LATEST})"
read -r -p "Continue? [y/N] " ans
[ "$ans" = "y" ] || [ "$ans" = "Y" ] || { echo "aborted"; exit 1; }

git tag -a "${TAG}" -m "Release ${TAG}"
git push origin "${TAG}"
echo "Pushed ${TAG}. Watch the build:  gh run watch  (Release workflow)"
