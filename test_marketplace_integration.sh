#!/usr/bin/env bash
# Test marketplace integration with real marketplace at localhost:8081 (behind /api)

set -e

echo "=== Testing Marketplace Integration ==="
echo ""

# The catalogue needs no credential: the till's only marketplace secret is
# its ADR-0116 device credential (the merchant-token route is retired,
# ut-docs#3577).

# 1. Test catalog endpoint
echo "1. Testing catalog fetch..."
CATALOG_RESPONSE=$(curl -s -k -X GET "https://127.0.0.1:8081/api/v1/catalog/plugins?arch=amd64&page=1&page_size=10")

echo "Catalog response preview:"
echo "$CATALOG_RESPONSE" | jq '.plugins[:2] | .[] | {id, name, version, vendor: .vendor.name}'

PLUGIN_COUNT=$(echo "$CATALOG_RESPONSE" | jq '.pagination.total_items // 0')
echo "✓ Catalog returned $PLUGIN_COUNT plugins"
echo ""

# 3. Test POS plugins store page
echo "3. Testing POS plugins store page..."
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/plugins/store)

if [ "$HTTP_STATUS" = "200" ]; then
  echo "✓ POS plugins store page accessible"
else
  echo "❌ POS plugins store page failed (HTTP $HTTP_STATUS)"
fi

echo ""
echo "=== Integration Test Complete ==="
