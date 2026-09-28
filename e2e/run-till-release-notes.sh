#!/usr/bin/env bash
# Boots a throwaway till for release-notes-3091.spec.ts (ut-docs#3091): built
# AS the newest version that has release notes (buildinfo.Version via
# -ldflags, exactly how release builds are stamped) and seeded as an existing
# shop updated from an older version, so the one-time "what's new" chip shows.
#
# Its own server + Playwright project rather than the shared default till:
# a stamped version and a pending chip in every other spec's status bar
# would move the ground under their assertions and screenshots.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
trap 'rm -rf "$DATA_DIR"' EXIT
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8097
# ut-docs#2704: never open the developer's browser on boot (the server's
# default); Playwright drives its own headless one. Overridable.
: "${UT_OPEN_BROWSER:=0}"; export UT_OPEN_BROWSER

cd "$ROOT"
TAG="$(find web/release-notes/en -name 'v*.md' -exec basename {} .md \; | sort -V | tail -n 1)"
VERSION="${TAG#v}"
go run ./e2e/seed_release_notes

# Build then run the BINARY from inside the fresh data dir, not the repo
# root — see the matching comment in run-till.sh for why.
BIN="$DATA_DIR/.ut-e2e-release-notes-bin"
go build -ldflags "-X github.com/universaltill/universal-till/internal/buildinfo.Version=${VERSION}" -o "$BIN" "$ROOT"
cd "$DATA_DIR"
exec "$BIN"
