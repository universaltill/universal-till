#!/usr/bin/env bash
# Boots a throwaway service-shop till in legacy template mode (shop.type=
# service, no template ledger; ADR-0190, ut-docs#6332) for
# template-menu-service-legacy-6332.spec.ts.
#
# It needs its own server + Playwright project rather than joining the
# shared default-project till because a service shop's layout changes what the
# MENU renders for every other spec on that server: it hides /tables and
# /kitchen-stations and re-labels /items, which would quietly move the ground
# under every menu/nav assertion in the suite.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
trap 'rm -rf "$DATA_DIR"' EXIT
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8094
# ut-docs#2704: never open the developer's browser on boot (the server's
# default); Playwright drives its own headless one. Overridable.
: "${UT_OPEN_BROWSER:=0}"; export UT_OPEN_BROWSER
# ut-docs#3814: no LAN discovery (mDNS) from a throwaway till. Overridable.
: "${UT_LAN_DISCOVERY:=0}"; export UT_LAN_DISCOVERY
# ut-docs#5237: never poll the real GitHub releases from a throwaway till —
# a new release would show "Update now" mid-run and move the status bar. Overridable.
: "${UT_UPDATE_CHECK:=0}"; export UT_UPDATE_CHECK
: "${UT_NEVER_REGISTER:=1}"; export UT_NEVER_REGISTER # ADR-0200: a throwaway till never registers itself in the cloud

cd "$ROOT"
go run ./e2e/seed_demo
# The layout comes from the service template's legacy_layout (the real
# shipped content); the seeder only sets the shop type — see
# e2e/seed_service_shop/main.go.
go run ./e2e/seed_service_shop

# Build then run the BINARY from inside the fresh data dir, not the repo
# root — see the matching comment in run-till.sh for why.
BIN="$DATA_DIR/.ut-e2e-layout-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
# shellcheck source=e2e/run-till-lib.sh
source "$ROOT/e2e/run-till-lib.sh"
ut_e2e_serve "$BIN"
