#!/usr/bin/env bash
# Boots a throwaway till for the diagnostic-mode indicator spec
# (diagnostic-mode-indicator-2169.spec.ts, ADR-0092 §7) — same idea as
# run-till.sh, but with UT_MARKETPLACE_ENDPOINT_URL pointed at the port the
# spec's own in-process fake ut-cloud listens on (127.0.0.1:8096), so the
# till can REALLY register (POST /v1/stores/register + signing key), REALLY
# redeem an activation code (POST /v1/stores/diagnostics/activate) and
# REALLY upload a batch on its cloudsync tick — the same request/response
# contract ut-cloud's DiagnosticsHandler documents, not a mocked route in
# the till.
#
# Its own server + Playwright project rather than the shared default till:
# registering the shared till with a fake cloud would change what the
# Registration card (and every cloudsync tick) does for every OTHER spec in
# the suite for the rest of the run.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
trap 'rm -rf "$DATA_DIR"' EXIT
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8095
export UT_MARKETPLACE_ENDPOINT_URL=http://127.0.0.1:8096

cd "$ROOT"
# Build then run the BINARY from inside the fresh data dir, not the repo
# root — see the matching comment in run-till.sh for why.
BIN="$DATA_DIR/.ut-e2e-diagnostics-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
exec "$BIN"
