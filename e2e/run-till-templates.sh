#!/usr/bin/env bash
# Boots a throwaway till for the shop-type template specs (ADR-0190,
# ut-docs#6332): template-matrix-6332.spec.ts and template-satellite-6332.spec.ts.
#
#   run-till-templates.sh PORT [SHOP_TYPE [PRIMARY_URL]]
#
# SHOP_TYPE is seeded WITHOUT a template ledger: a shop that predates
# templates (legacy mode), so the matrix spec can drive the existing-shop
# offer first. PRIMARY_URL makes it an additional till (type locked).
# Own server + Playwright project because a shop type changes what the Menu
# and Settings render for every other spec on a shared till.
set -euo pipefail
PORT="${1:?port}"; SHOP_TYPE="${2:-}"; PRIMARY_URL="${3:-}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
trap 'rm -rf "$DATA_DIR"' EXIT
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR="127.0.0.1:${PORT}"
: "${UT_OPEN_BROWSER:=0}"; export UT_OPEN_BROWSER
: "${UT_LAN_DISCOVERY:=0}"; export UT_LAN_DISCOVERY
: "${UT_UPDATE_CHECK:=0}"; export UT_UPDATE_CHECK
: "${UT_NEVER_REGISTER:=1}"; export UT_NEVER_REGISTER # ADR-0200
export UT_E2E_SHOP_TYPE="$SHOP_TYPE" UT_E2E_PRIMARY_URL="$PRIMARY_URL"

cd "$ROOT"
go run ./e2e/seed_demo
go run ./e2e/seed_template_shop

# Binary run from inside the fresh data dir, see run-till.sh.
BIN="$DATA_DIR/.ut-e2e-templates-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
# shellcheck source=e2e/run-till-lib.sh
source "$ROOT/e2e/run-till-lib.sh"
ut_e2e_serve "$BIN"
