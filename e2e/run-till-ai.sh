#!/usr/bin/env bash
# Boots a throwaway till for the ai.identify overlay spec — same idea as
# run-till.sh, but with UT_AI_ENDPOINT set so `.aiIdentify` resolves true
# and the server actually renders #ai-identify-open/#ai-identify-overlay
# (internal/pages/index_page.go, gated on ai.Service.Enabled()). Unlike the
# barcode-scan overlay, ai-identify's markup doesn't exist in the DOM at
# all when the feature is off (web/ui/pages/index.html's `{{ if .aiIdentify }}`),
# so it needs its own server + Playwright project rather than joining the
# shared default-project till everything else drives (ut-docs#1559).
#
# The endpoint is a real-looking but non-routable address: err.name
# branching (this spec's whole point) happens inside the getUserMedia
# .catch(), before any actual identify API call — no request to this
# address is ever made by the tests here.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
trap 'rm -rf "$DATA_DIR"' EXIT
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8093
export UT_AI_ENDPOINT=http://127.0.0.1:1

cd "$ROOT"
go run ./e2e/seed_demo

# Build then run the BINARY from inside the fresh data dir, not the repo
# root — see the matching comment in run-till.sh for why.
BIN="$DATA_DIR/.ut-e2e-ai-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
exec "$BIN"
