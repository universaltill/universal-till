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
# ut-docs#2704: never open the developer's browser on boot (the server's
# default); Playwright drives its own headless one. Overridable.
: "${UT_OPEN_BROWSER:=0}"; export UT_OPEN_BROWSER
# ut-docs#3814: no LAN discovery (mDNS) from a throwaway till. Overridable.
: "${UT_LAN_DISCOVERY:=0}"; export UT_LAN_DISCOVERY
# ut-docs#5237: never poll the real GitHub releases from a throwaway till —
# a new release would show "Update now" mid-run and move the status bar. Overridable.
: "${UT_UPDATE_CHECK:=0}"; export UT_UPDATE_CHECK
: "${UT_NEVER_REGISTER:=1}"; export UT_NEVER_REGISTER # ADR-0200: a throwaway till never registers itself in the cloud
# ADR-0200 (ut-docs#6559): the ONE launcher whose till really registers itself,
# against the fake cloud below. UT_NEVER_REGISTER stays 1 (guard-never-register);
# the seam waives it only for a source build whose marketplace endpoint is
# loopback, and never demo/stopped/notice/country. The specs name the shop and
# open Settings (which shows the notice) via ensureRegisteredAutomatically.
export UT_E2E_AUTO_REGISTER=1
export UT_MARKETPLACE_ENDPOINT_URL=http://127.0.0.1:8096

cd "$ROOT"
# Build then run the BINARY from inside the fresh data dir, not the repo
# root — see the matching comment in run-till.sh for why.
BIN="$DATA_DIR/.ut-e2e-diagnostics-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
# shellcheck source=e2e/run-till-lib.sh
source "$ROOT/e2e/run-till-lib.sh"
ut_e2e_serve "$BIN"
