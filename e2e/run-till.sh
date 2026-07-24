#!/usr/bin/env bash
# Boots a throwaway till for the e2e suite (fresh DB each run, demo catalog
# seeded by the migrations, auth off so specs drive the UI directly).
set -euo pipefail
cd "$(dirname "$0")/.."
DATA_DIR="$(mktemp -d)"
trap 'rm -rf "$DATA_DIR"' EXIT
export UT_DATA_DIR="$DATA_DIR" UT_AUTH=off UT_LISTEN_ADDR=127.0.0.1:8091
# Installs the real FAQ plugin (page entry + content bundles) so faq.spec.ts
# can drive an actually-installed plugin page, not a mocked route.
go run ./e2e/seed_faq
exec go run .
