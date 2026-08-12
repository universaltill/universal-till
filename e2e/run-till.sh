#!/usr/bin/env bash
# Boots a throwaway till for the e2e suite (fresh DB each run, demo catalog
# seeded explicitly below — since ut-docs#539 the migrations no longer seed
# it — auth off so specs drive the UI directly).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="$(mktemp -d)"
trap 'rm -rf "$DATA_DIR"' EXIT
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8091

cd "$ROOT"
# Installs the real FAQ plugin (page entry + content bundles) so faq.spec.ts
# can drive an actually-installed plugin page, not a mocked route. Talks to
# the DB directly (internal/db.Open), not through config/paths, so it's
# unaffected by the CWD concern below.
go run ./e2e/seed_faq

# Restores the demo catalogue the specs scan (ut-docs#539 made it opt-in;
# this runs the same seed the setup wizard's checkbox runs).
go run ./e2e/seed_demo

# Build once, then run the BINARY (not `go run`) from INSIDE the fresh data
# dir, not the repo root. `go run` (even with `-C`) ties the process's
# working directory to the module root — and the app's one-time legacy-data
# migration (internal/paths.migrateLegacyDB) looks for ./data/unitill-pos.db
# relative to CWD. Running from the repo root silently copies a real local
# dev database into what's supposed to be a throwaway till (confirmed live
# 2026-07-29: a from-scratch install test landed on a login screen with a
# real operator's PIN already set, because it inherited that operator's
# actual local ~/repos/.../data/unitill-pos.db). Running the built binary
# from the empty temp dir means that path can never resolve to anything.
BIN="$DATA_DIR/.ut-e2e-bin"
go build -o "$BIN" "$ROOT"
cd "$DATA_DIR"
exec "$BIN"
