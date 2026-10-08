#!/usr/bin/env bash
# Boots a throwaway till for incoming-release-notes-3940.spec.ts
# (ut-docs#3940): built AS the newest version that has release notes, and
# pointed (UT_UPDATE_RELEASES_URL) at a local fake releases API
# (fake-release-server.mjs) that offers two synthetic newer versions — one
# skipped, one offered — whose release-notes.json is built by the real
# bundle tool from web/release-notes plus those two synthetic notes.
#
# Its own server + Playwright project: an "update available" status in the
# shared default till would change every other spec's status bar.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
FAKE_PORT=8100
FAKE_PID=""
TILL_PID=""
cleanup() {
  if [ -n "$TILL_PID" ]; then kill "$TILL_PID" 2>/dev/null || true; fi
  if [ -n "$FAKE_PID" ]; then kill "$FAKE_PID" 2>/dev/null || true; fi
  rm -rf "$DATA_DIR"
}
trap cleanup EXIT
trap 'exit 143' INT TERM
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8099
export UT_UPDATE_RELEASES_URL="http://127.0.0.1:${FAKE_PORT}/latest"
# ut-docs#2704: never open the developer's browser on boot (the server's
# default); Playwright drives its own headless one. Overridable.
: "${UT_OPEN_BROWSER:=0}"; export UT_OPEN_BROWSER

cd "$ROOT"
TAG="$(find web/release-notes/en -name 'v*.md' -exec basename {} .md \; | sort -V | tail -n 1)"
RUNNING="${TAG#v}"
IFS=. read -r MA MI PA <<<"$RUNNING"
SKIPPED="${MA}.${MI}.$((PA + 1))"
OFFERED="${MA}.$((MI + 1)).0"

# The real notes plus the two synthetic newer releases.
NOTES="$DATA_DIR/release-notes"
mkdir -p "$NOTES"
cp -R web/release-notes/. "$NOTES/"
for V in "$SKIPPED" "$OFFERED"; do
  printf -- '---\nversion: v%s\ndate: 2026-10-08\n---\n## New\n\n- Synthetic e2e note for v%s.\n' "$V" "$V" >"$NOTES/en/v${V}.md"
done
go run ./scripts/release-notes-bundle -dir "$NOTES" -o "$DATA_DIR/release-notes.json"

node "$ROOT/e2e/fake-release-server.mjs" "$FAKE_PORT" "$DATA_DIR/release-notes.json" "$RUNNING" "$OFFERED" "$SKIPPED" &
FAKE_PID=$!

# Setup complete (so /settings is reachable without the wizard).
go run ./e2e/seed_release_notes

# Build then run the BINARY from inside the fresh data dir, not the repo
# root — see the matching comment in run-till.sh for why.
BIN="$DATA_DIR/.ut-e2e-incoming-notes-bin"
go build -ldflags "-X github.com/universaltill/universal-till/internal/buildinfo.Version=${RUNNING}" -o "$BIN" "$ROOT"
cd "$DATA_DIR"
"$BIN" &
TILL_PID=$!
wait "$TILL_PID"
