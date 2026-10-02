#!/usr/bin/env bash
# Boots a throwaway till for csp-report-only-2913.spec.ts (ut-docs#2913,
# slice 1): the same fresh-DB, demo-catalogue, auth-off till as run-till.sh,
# plus UT_CSP_REPORT_ONLY=1 so every response carries the report-only
# Content-Security-Policy and /csp-report collects the violations.
#
# Its own server + Playwright project rather than the shared default till:
# with the flag on, every page logs report-only violations as console
# errors by design, which would trip watchConsole in every other spec.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
trap 'rm -rf "$DATA_DIR"' EXIT
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8098 UT_CSP_REPORT_ONLY=1
# ut-docs#2704: never open the developer's browser on boot (the server's
# default); Playwright drives its own headless one. Overridable.
: "${UT_OPEN_BROWSER:=0}"; export UT_OPEN_BROWSER

cd "$ROOT"
# The demo catalogue the sale screen and Items page render (ut-docs#539
# made it opt-in; same seed the setup wizard's checkbox runs). Talks to the
# DB directly (internal/db.Open), unaffected by the CWD concern below.
go run ./e2e/seed_demo

# Build once, then run the BINARY (not `go run`) from INSIDE the fresh data
# dir, not the repo root: the app's one-time legacy-data migration
# (internal/paths.migrateLegacyDB) looks for ./data/unitill-pos.db relative
# to CWD, so running from the repo root would silently copy a real local
# dev database into this throwaway till. From the empty temp dir that path
# can never resolve to anything. Full story: the matching comment in
# run-till.sh.
BIN="$DATA_DIR/.ut-e2e-csp-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
exec "$BIN"
