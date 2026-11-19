#!/usr/bin/env bash
#
# The one home for "latest stable release tag" and the version bump built on
# it (ut-docs#1807). Pre-release tags (-beta.N, -rc1, -nightly.*, ...) never
# count as latest: only a plain vMAJOR.MINOR.PATCH tag does.
#
# Usage: latest-stable-tag.sh [--next major|minor|patch] [DIR]
#   (no flag)  print the highest stable tag with its "v" (e.g. v0.36.0);
#              print nothing, exit 0, when there is none
#   --next K   print the next version WITHOUT the "v" (e.g. 0.36.1), bumped
#              from the latest stable tag, or from 0.0.0 when there is none.
#              This is the only bump implementation: release.yml and
#              scripts/release.sh call it.
#   DIR        git repo to read (default: the current directory); a DIR that
#              is not a repo is an error (fail closed, never an empty "latest").
#
# Order is numeric (v0.10.0 > v0.9.0), portable: no GNU `sort -V`.
set -euo pipefail

usage() { echo "usage: latest-stable-tag.sh [--next major|minor|patch] [DIR]" >&2; exit 2; }

KIND=""
if [ "${1:-}" = "--next" ]; then
  [ $# -ge 2 ] || usage
  KIND="$2"
  case "$KIND" in major | minor | patch) ;; *) usage ;; esac
  shift 2
fi
[ $# -le 1 ] || usage
DIR="${1:-.}"

# Into a variable first: a failing git must exit non-zero, not look like
# "no tags".
TAGS="$(git -C "$DIR" tag -l 'v*')"
LATEST="$(printf '%s\n' "$TAGS" | { grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true; } \
  | sed 's/^v//' | sort -t. -k1,1n -k2,2n -k3,3n | tail -n1)"

if [ -z "$KIND" ]; then
  [ -z "$LATEST" ] || echo "v${LATEST}"
  exit 0
fi

IFS=. read -r MA MI PA <<< "${LATEST:-0.0.0}"
case "$KIND" in
  # 10#: a leading-zero component (v0.1.08) is decimal, not octal.
  major) MA=$((10#$MA + 1)); MI=0; PA=0 ;;
  minor) MA=$((10#$MA)); MI=$((10#$MI + 1)); PA=0 ;;
  patch) MA=$((10#$MA)); MI=$((10#$MI)); PA=$((10#$PA + 1)) ;;
esac
echo "${MA}.${MI}.${PA}"
