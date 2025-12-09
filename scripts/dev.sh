#!/usr/bin/env bash
# Development startup script - loads pos.env.dev and runs the app

set -e

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# Load development environment variables
if [ -f "pos.env.dev" ]; then
    echo "Loading development environment from pos.env.dev..."
    set -a  # automatically export all variables
    source <(grep -v '^#' pos.env.dev | grep -v '^$')
    set +a
    # Tell the app to load pos.env.dev
    export UT_ENV_FILE=pos.env.dev
else
    echo "Warning: pos.env.dev not found, using defaults"
fi

# Check if mock marketplace should be running
if [ "${UT_MARKETPLACE_ENDPOINT_URL:-}" = "http://localhost:8082" ] || [ "${UT_MARKETPLACE_ENDPOINT_URL:-}" = "http://127.0.0.1:8082" ]; then
    # Check if mock is running
    if ! nc -z localhost 8082 2>/dev/null; then
        echo ""
        echo "⚠️  Mock marketplace is not running on port 8082"
        echo "   Start it in another terminal with:"
        echo "   go run scripts/mock-marketplace/main.go"
        echo ""
    else
        echo "✓ Mock marketplace detected on port 8082"
    fi
fi

# Build and run
echo "Building Universal Till POS..."
make build

echo ""
echo "Starting Universal Till POS on ${UT_LISTEN_ADDR:-:8080}..."
echo "Marketplace endpoint: ${UT_MARKETPLACE_ENDPOINT_URL:-http://127.0.0.1:8081}"
echo "Marketplace client ID: ${UT_MARKETPLACE_CLIENT_ID:-(not set)}"
echo "Marketplace locale: ${UT_MARKETPLACE_LOCALE:-en-US}"
echo ""

./bin/unitill-pos
