#!/usr/bin/env bash
# Boots a throwaway till for the manual's `claim` screenshot (ut-docs#5127
# UT-1b): the move approval page, /settings/cloud/move/{id}, with a move
# really waiting for this till's approval. Same idea as run-till.sh, plus
# store credentials (env) and UT_MARKETPLACE_ENDPOINT_URL pointed at
# fake-move-cloud.mjs, whose sync answer carries one fixed pending move —
# the till picks it up on its start-up check-in through the real cloudsync
# path, not a mocked route.
#
# Its own server: a pending move puts the move banner on every page but the
# sale screen, which must never reach any other topic's screenshot.
# Only playwright.docs.config.ts boots it.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
FAKE_PORT=8101
FAKE_PID=""
TILL_PID=""
cleanup() {
  if [ -n "$TILL_PID" ]; then kill "$TILL_PID" 2>/dev/null || true; fi
  if [ -n "$FAKE_PID" ]; then kill "$FAKE_PID" 2>/dev/null || true; fi
  rm -rf "$DATA_DIR"
}
trap cleanup EXIT
trap 'exit 143' INT TERM
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8093
# ut-docs#2704: never open the developer's browser on boot (the server's
# default); Playwright drives its own headless one. Overridable.
: "${UT_OPEN_BROWSER:=0}"; export UT_OPEN_BROWSER
# ut-docs#3814: no LAN discovery (mDNS) from a throwaway till. Overridable.
: "${UT_LAN_DISCOVERY:=0}"; export UT_LAN_DISCOVERY
# ut-docs#5237: never poll the real GitHub releases from a throwaway till —
# a new release would show "Update now" mid-run and move the status bar. Overridable.
: "${UT_UPDATE_CHECK:=0}"; export UT_UPDATE_CHECK
# A registered (unpaid) till in a made-up store, talking only to the fake.
export UT_MARKETPLACE_ENDPOINT_URL="http://127.0.0.1:${FAKE_PORT}"
export UT_MARKETPLACE_STORE_ID=docs-store
export UT_MARKETPLACE_DEVICE_ID=docs-device
export UT_MARKETPLACE_MERCHANT_TOKEN=docs-token
# Must equal MOVE_ID in e2e/tests-docs/docs-shots.spec.ts.
MOVE_ID="5127d0c5-0000-4000-8000-000000000001"

cd "$ROOT"
node "$ROOT/e2e/fake-move-cloud.mjs" "$FAKE_PORT" "$UT_MARKETPLACE_STORE_ID" "$MOVE_ID" &
FAKE_PID=$!

# Same demo catalogue as the default docs till (and setup marked complete).
go run ./e2e/seed_demo

# Build then run the BINARY from inside the fresh data dir, not the repo
# root — see the matching comment in run-till.sh for why.
BIN="$DATA_DIR/.ut-e2e-move-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
"$BIN" &
TILL_PID=$!
wait "$TILL_PID"
