#!/usr/bin/env bash
# Idempotently add/replace one artifact's SHA-256 line in a goreleaser-style
# checksums.txt (format: "<sha256>  <filename>", two spaces — sha256sum
# compatible, per .goreleaser.yaml's checksum.name_template).
#
# Why this exists: goreleaser only checksums its OWN artifacts. The macOS
# .dmg is built by a separate release.yml job (macos-app) on a dedicated
# macOS runner and uploaded after the fact, so it never lands in
# checksums.txt on its own. internal/selfupdate's mac update path
# (macapp_darwin.go) verifies the downloaded .dmg against checksums.txt
# before ever mounting it, failing closed if the entry is missing or
# mismatched — so the macos-app job must fold the dmg's checksum in here
# after building it, or every in-app mac update would be refused.
#
# Usage: packaging/macos/update-checksums.sh <checksums-file> <artifact-file> [artifact-name]
#   checksums-file  path to checksums.txt (created if it doesn't exist)
#   artifact-file   path to the file to hash
#   artifact-name   name recorded in checksums.txt (default: basename of artifact-file)
#
# Idempotent: re-running for the same artifact-name replaces its line rather
# than appending a duplicate (safe for release-workflow re-runs).
set -euo pipefail

CHECKSUMS="${1:?usage: update-checksums.sh <checksums-file> <artifact-file> [artifact-name]}"
ARTIFACT="${2:?usage: update-checksums.sh <checksums-file> <artifact-file> [artifact-name]}"
NAME="${3:-$(basename "$ARTIFACT")}"

[ -f "$ARTIFACT" ] || { echo "update-checksums.sh: artifact not found: $ARTIFACT" >&2; exit 1; }

if command -v shasum >/dev/null 2>&1; then
  SHA="$(shasum -a 256 "$ARTIFACT" | awk '{print $1}')"
elif command -v sha256sum >/dev/null 2>&1; then
  SHA="$(sha256sum "$ARTIFACT" | awk '{print $1}')"
else
  echo "update-checksums.sh: neither shasum nor sha256sum found" >&2
  exit 1
fi

touch "$CHECKSUMS"
# Drop any existing line for this exact filename (idempotent re-runs), keep
# everything else untouched, then append the fresh line.
TMP="$(mktemp)"
awk -v name="$NAME" '$2 != name' "$CHECKSUMS" > "$TMP"
printf '%s  %s\n' "$SHA" "$NAME" >> "$TMP"
mv "$TMP" "$CHECKSUMS"

echo "update-checksums.sh: ${NAME} -> ${SHA}"
