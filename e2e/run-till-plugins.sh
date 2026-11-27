#!/usr/bin/env bash
# Docs-shots ONLY (ut-docs#335): a throwaway till whose marketplace catalogue
# is a fixed offline snapshot (e2e/seed_docs_plugin_catalog), so the plugins
# topic's Plugin Store screenshots show a populated store. No e2e spec boots
# this till. It is separate from run-till.sh because the catalogue is also
# read by /plugins, the status-bar update chip and the update scheduler:
# seeding the shared docs till could change other topics' pictures.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
trap 'rm -rf "$DATA_DIR"' EXIT
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8107
: "${UT_OPEN_BROWSER:=0}"; export UT_OPEN_BROWSER
: "${UT_LAN_DISCOVERY:=0}"; export UT_LAN_DISCOVERY
: "${UT_UPDATE_CHECK:=0}"; export UT_UPDATE_CHECK
: "${UT_NEVER_REGISTER:=1}"; export UT_NEVER_REGISTER # ADR-0200: a throwaway till never registers itself in the cloud
# Nothing listens on port 1, so every background catalogue refresh fails at
# once and the seeded snapshot stays (a developer's local cloud on the default
# endpoint could otherwise replace it).
export UT_MARKETPLACE_ENDPOINT_URL=http://127.0.0.1:1/api

cd "$ROOT"
go run ./e2e/seed_faq
go run ./e2e/seed_demo
go run ./e2e/seed_docs_plugin_catalog

# Run the built binary from inside the fresh data dir, as run-till.sh does
# (see the CWD note there).
BIN="$DATA_DIR/.ut-e2e-plugins-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
# shellcheck source=e2e/run-till-lib.sh
source "$ROOT/e2e/run-till-lib.sh"
ut_e2e_serve "$BIN"
