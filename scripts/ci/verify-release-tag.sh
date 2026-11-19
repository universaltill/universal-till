#!/usr/bin/env bash
# Verifies the release tag play-publish.yml is about to build and sign
# (ut-docs#6410, ADR-0180). Runs from the MAIN checkout of the workflow, so a
# tag can never carry its own version of this check.
#   verify-release-tag.sh X.Y.Z   -> prints the commit SHA of tag vX.Y.Z
# Fails unless X.Y.Z is plain semver, the tag exists, and its commit is an
# ancestor of $MAIN_REF (default origin/main).
set -euo pipefail
v="${1:-}"
[[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "::error::'$v' is not X.Y.Z" >&2; exit 1; }
main_ref="${MAIN_REF:-origin/main}"
sha="$(git rev-parse --verify --quiet "refs/tags/v$v^{commit}")" || { echo "::error::tag v$v does not exist" >&2; exit 1; }
git merge-base --is-ancestor "$sha" "$main_ref" || { echo "::error::tag v$v ($sha) is not on $main_ref; refusing to publish" >&2; exit 1; }
printf '%s\n' "$sha"
