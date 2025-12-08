#!/usr/bin/env bash
# Test marketplace integration with real marketplace at localhost:8081

set -e

echo "=== Testing Marketplace Integration ==="
echo ""

# 1. Test authentication endpoint
echo "1. Testing JWT authentication..."
AUTH_RESPONSE=$(curl -s -k -X POST https://127.0.0.1:8081/v1/auth/merchant-token \
  -H "Content-Type: application/json" \
  -d '{
    "merchant_id": "pos-client",
    "device_id": "dev-secret",
    "pos_version": "0.1.0"
  }')

echo "Auth response: $AUTH_RESPONSE"
TOKEN=$(echo "$AUTH_RESPONSE" | jq -r '.token // empty')

if [ -z "$TOKEN" ]; then
  echo "❌ Failed to get JWT token"
  exit 1
fi

echo "✓ JWT token received: ${TOKEN:0:20}..."
echo ""

# 2. Test catalog endpoint
echo "2. Testing catalog fetch..."
CATALOG_RESPONSE=$(curl -s -k -X GET "https://127.0.0.1:8081/v1/catalog/plugins?arch=amd64&page=1&page_size=10" \
  -H "Authorization: Bearer $TOKEN")

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
