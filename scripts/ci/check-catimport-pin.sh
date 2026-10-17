#!/usr/bin/env bash
#
# Version-drift check for the shared catimport module (ADR-0164 §1 "mirror
# contract", ut-docs#4525). ut-cloud requires a published
# catimport/vX.Y.Z tag of github.com/universaltill/universal-till/catimport;
# this fails when universal-till's catimport/ has changed since that tag,
# so a parser fix can't reach the till without a tag and a ut-cloud bump.
# "Changed" means its Go source, go.mod or go.sum.
#
# Run by ut-cloud's manifest-contract-guard job against a full-history
# checkout of universal-till main (tags fetched):
#   pinned="$(go list -m -f '{{.Version}}' github.com/universaltill/universal-till/catimport)"
#   bash universal-till/scripts/ci/check-catimport-pin.sh "${pinned}" universal-till
#
# Args: <pinned version, e.g. v0.1.0> [universal-till checkout, default .]
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "usage: $0 <pinned-version> [universal-till-dir]" >&2
  exit 2
fi
VERSION="$1"
REPO="${2:-.}"

if [[ ! "${VERSION}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "❌ catimport pin: '${VERSION}' is not a release version — pin a catimport/vX.Y.Z tag, never a pseudo-version or a replace" >&2
  exit 1
fi
TAG="catimport/${VERSION}"
if ! git -C "${REPO}" rev-parse --verify --quiet "refs/tags/${TAG}^{commit}" >/dev/null; then
  echo "❌ catimport pin: tag ${TAG} not found in ${REPO} (is it pushed, and were tags fetched?)" >&2
  exit 1
fi
# Only what a consumer compiles counts: Go source and the module files (a
# README edit needs no release).
changed="$(git -C "${REPO}" diff --name-only "${TAG}" HEAD -- \
  ':(glob)catimport/**/*.go' catimport/go.mod catimport/go.sum)"
if [[ -n "${changed}" ]]; then
  echo "❌ catimport pin: universal-till's catimport/ changed since ${TAG} — tag the next catimport/vX.Y.Z and bump the pin:" >&2
  printf '%s\n' "${changed}" | sed 's/^/  /' >&2
  exit 1
fi
echo "✅ catimport pin: ${TAG} matches universal-till's catimport/"
