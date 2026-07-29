#!/usr/bin/env bash
# Boots a throwaway till for the login/setup e2e spec — same idea as
# run-till.sh, but WITHOUT UT_AUTH=off, on its own port, so it goes
# through the real first-boot wizard + PIN login instead of bypassing
# auth entirely. Kept as a separate server (and Playwright project) so
# it doesn't interfere with the shared, already-logged-in-by-default
# server every other spec drives.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
trap 'rm -rf "$DATA_DIR"' EXIT
export UT_DATA_DIR="$DATA_DIR" UT_LISTEN_ADDR=127.0.0.1:8092

# Build then run the BINARY from inside the fresh data dir, not the repo
# root — see the matching comment in run-till.sh for why: `go run` ties
# the process's CWD to the module root, and the app's one-time legacy-data
# migration looks for ./data/unitill-pos.db relative to CWD, which
# silently pulls in a real local dev database (with real operator PINs
# already set) instead of staying a genuinely first-boot install. This
# spec specifically needs a genuine first-boot state to drive the setup
# wizard, so it's the one place this would be most confusing to get wrong.
BIN="$DATA_DIR/.ut-e2e-auth-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
exec "$BIN"
