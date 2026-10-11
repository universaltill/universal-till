#!/usr/bin/env bash
# Boots a throwaway till for photo-cutout-3127.spec.ts (ut-docs#3127): the
# same fresh-DB, demo-catalogue, auth-off till as run-till.sh, with
# background removal configured (UT_AI_IMAGE_ENDPOINT + an allowed model)
# against a local fake rembg server (fake-rembg-server.mjs), so the item,
# category and variant photo pickers offer "Remove background".
#
# Its own server + Playwright project: the shared default till must keep
# rendering the pickers exactly as a shop without AI sees them
# (photo-cutout-off-3127.spec.ts pins that).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
FAKE_PORT=8102
FAKE_PID=""
TILL_PID=""
cleanup() {
  if [ -n "$TILL_PID" ]; then kill "$TILL_PID" 2>/dev/null || true; fi
  if [ -n "$FAKE_PID" ]; then kill "$FAKE_PID" 2>/dev/null || true; fi
  rm -rf "$DATA_DIR"
}
trap cleanup EXIT
trap 'exit 143' INT TERM
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8101
export UT_AI_IMAGE_ENDPOINT="http://127.0.0.1:${FAKE_PORT}" UT_AI_IMAGE_MODEL=u2netp
# ut-docs#2704: never open the developer's browser on boot (the server's
# default); Playwright drives its own headless one. Overridable.
: "${UT_OPEN_BROWSER:=0}"; export UT_OPEN_BROWSER

node "$ROOT/e2e/fake-rembg-server.mjs" "$FAKE_PORT" &
FAKE_PID=$!

cd "$ROOT"
# The demo catalogue the Items page renders (same as run-till.sh).
go run ./e2e/seed_demo

# Build then run the BINARY from inside the fresh data dir, not the repo
# root — see the matching comment in run-till.sh for why.
BIN="$DATA_DIR/.ut-e2e-cutout-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
"$BIN" &
TILL_PID=$!
wait "$TILL_PID"
