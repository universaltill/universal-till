#!/usr/bin/env bash
# Boots a throwaway till for the notices spec (notices-5366.spec.ts, ADR-0176,
# ut-docs#5366): same idea as run-till-move.sh -- store credentials in the
# environment and UT_MARKETPLACE_ENDPOINT_URL pointed at fake-notices-cloud.mjs,
# so the till pulls notices through the REAL cloudsync path (request, re-
# validation, mirror, acks), not a mocked route. UT_TEST_CLOUDSYNC_TICK_MS
# makes it check in every second so a spec can watch a notice arrive.
#
# Its own server + Playwright project: a registered till talking to a fake
# cloud changes what every cloudsync tick does, which must not leak into the
# shared default till.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
FAKE_PORT=8104
FAKE_PID=""
TILL_PID=""
cleanup() {
  if [ -n "$TILL_PID" ]; then kill "$TILL_PID" 2>/dev/null || true; fi
  if [ -n "$FAKE_PID" ]; then kill "$FAKE_PID" 2>/dev/null || true; fi
  rm -rf "$DATA_DIR"
}
trap cleanup EXIT
trap 'exit 143' INT TERM
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8103
: "${UT_OPEN_BROWSER:=0}"; export UT_OPEN_BROWSER
: "${UT_LAN_DISCOVERY:=0}"; export UT_LAN_DISCOVERY
: "${UT_UPDATE_CHECK:=0}"; export UT_UPDATE_CHECK
export UT_TEST_CLOUDSYNC_TICK_MS=1000
export UT_MARKETPLACE_ENDPOINT_URL="http://127.0.0.1:${FAKE_PORT}"
export UT_MARKETPLACE_STORE_ID=notices-store
export UT_MARKETPLACE_DEVICE_ID=notices-device
export UT_MARKETPLACE_MERCHANT_TOKEN=notices-token

cd "$ROOT"
node "$ROOT/e2e/fake-notices-cloud.mjs" "$FAKE_PORT" &
FAKE_PID=$!

go run ./e2e/seed_demo

BIN="$DATA_DIR/.ut-e2e-notices-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
"$BIN" &
TILL_PID=$!
wait "$TILL_PID"
