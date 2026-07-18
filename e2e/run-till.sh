#!/usr/bin/env bash
# Boots a throwaway till for the e2e suite (fresh DB each run, demo catalog
# seeded by the migrations, auth off so specs drive the UI directly).
set -euo pipefail
cd "$(dirname "$0")/.."
DATA_DIR="$(mktemp -d)"
trap 'rm -rf "$DATA_DIR"' EXIT
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8091
exec go run .
