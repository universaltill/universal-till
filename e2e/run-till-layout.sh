#!/usr/bin/env bash
# Boots a throwaway till with the real plugins/layout-salon `layout` plugin
# installed (ADR-0088, ut-docs#1904), for layout-plugin-menu-1904.spec.ts.
#
# It needs its own server + Playwright project rather than joining the
# shared default-project till for the same reason run-till-ai.sh does: the
# amendments change what the MENU renders for every other spec on that
# server. Installing a plugin that hides /tables and /kitchen-stations and
# re-labels /items into the till the whole default project drives would
# quietly move the ground under every menu/nav assertion in the suite.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
trap 'rm -rf "$DATA_DIR"' EXIT
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8094

cd "$ROOT"
go run ./e2e/seed_demo
# Installs the REAL plugin from plugins/layout-salon (manifest + locales),
# not a fixture copy — see e2e/seed_layout_salon/main.go.
go run ./e2e/seed_layout_salon

# Build then run the BINARY from inside the fresh data dir, not the repo
# root — see the matching comment in run-till.sh for why.
BIN="$DATA_DIR/.ut-e2e-layout-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
exec "$BIN"
