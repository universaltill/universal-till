#!/usr/bin/env bash
# Release-notes guard (ut-docs#3091): a release needs its owner-facing
# "What's new" note — web/release-notes/en/<tag>.md — the one Settings →
# About shows and the GitHub Release body leads with. release.yml's
# `prepare` job runs this BEFORE a tag is created (workflow_dispatch) or
# built (tag push), so a release without notes is refused, not shipped
# silently.
#
# Usage: guard-release-notes.sh <tag> [--header <out-file>]
#   <tag>          vMAJOR.MINOR.PATCH (e.g. v0.30.6)
#   --header FILE  also write the Release-body header goreleaser prepends
#                  (--release-header): "## What's new" + the note's body.
# UT_RELEASE_NOTES_DIR overrides the notes root (tests).
set -euo pipefail

usage() { echo "usage: $0 <vX.Y.Z> [--header <out-file>]" >&2; exit 2; }

[ "$#" -ge 1 ] || usage
TAG="$1"; shift
HEADER=""
while [ "$#" -gt 0 ]; do
    case "$1" in
        --header) [ "$#" -ge 2 ] || usage; HEADER="$2"; shift 2 ;;
        *) usage ;;
    esac
done

ROOT="${UT_RELEASE_NOTES_DIR:-$(cd "$(dirname "$0")/../.." && pwd)/web/release-notes}"
FILE="${ROOT}/en/${TAG}.md"

if ! [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "::error::tag '${TAG}' must be vMAJOR.MINOR.PATCH"
    exit 1
fi
if [ ! -f "$FILE" ]; then
    echo "::error::no release notes for ${TAG}: web/release-notes/en/${TAG}.md is missing."
    echo "::error::Draft them from the Done cards' close-out comments since the previous tag (RELEASING.md), merge to main, then release."
    exit 1
fi

# Read it the way internal/releasenotes does: CRLF line ends and trailing
# blanks on the front-matter values are tolerated, not refused.
CONTENT="$(tr -d '\r' < "$FILE")"
# Front-matter: first line '---', then version/date, then '---'.
if [ "$(sed -n 1p <<<"$CONTENT")" != "---" ]; then
    echo "::error::${FILE}: must start with a '---' front-matter block (version, date)"
    exit 1
fi
# No early `exit` in awk: under pipefail that can SIGPIPE printf (ut-docs#2983).
FRONT="$(printf '%s\n' "$CONTENT" | awk 'NR==1{next} d{next} /^---$/{d=1; next} {print}')"
BODY="$(printf '%s\n' "$CONTENT" | awk 'NR==1{next} f{print} /^---$/ && !f{f=1}')"
VERSION="$(printf '%s\n' "$FRONT" | sed -n 's/^version:[[:space:]]*//p' | sed -n 1p | sed 's/[[:space:]]*$//')"
DATE="$(printf '%s\n' "$FRONT" | sed -n 's/^date:[[:space:]]*//p' | sed -n 1p | sed 's/[[:space:]]*$//')"

if [ "$VERSION" != "$TAG" ]; then
    echo "::error::${FILE}: front-matter version '${VERSION}' does not match the tag ${TAG}"
    exit 1
fi
if ! [[ "$DATE" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
    echo "::error::${FILE}: front-matter date '${DATE}' must be YYYY-MM-DD"
    exit 1
fi
if [ -z "$(printf '%s' "$BODY" | tr -d '[:space:]')" ]; then
    echo "::error::${FILE}: the note has no text"
    exit 1
fi

if [ -n "$HEADER" ]; then
    {
        printf "## What's new\n\n"
        printf '%s\n' "$BODY" | sed -e 's/^## /### /'
    } > "$HEADER"
fi
echo "release notes for ${TAG}: ok (${FILE#"$ROOT"/})"
