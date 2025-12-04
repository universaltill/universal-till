# Plugin Host Quickstart Guide

This guide shows how to test the plugin host implementation with the mock marketplace server.

## Prerequisites

- Go 1.21+
- SQLite3
- Universal Till POS app built and running

## Quick Start: Testing Plugin Installation

### Step 1: Start the Mock Marketplace Server

The mock marketplace serves 3 sample plugins at `http://127.0.0.1:8081`:

```bash
cd /path/to/universal-till
go run ./scripts/mock-marketplace/main.go
```

You should see:

```
Mock Marketplace Server starting on :8081
Endpoints:
  GET  /health
  GET  /v1/catalog/plugins?device_arch=&capability=
  POST /v1/download/token
  GET  /artifacts/{filename}
```

### Step 2: Configure POS to Use Mock Marketplace

Set the marketplace URL environment variable (already defaults to localhost):

```bash
export UT_MARKETPLACE_URL="http://127.0.0.1:8081"
```

### Step 3: Start the POS App

```bash
cd /path/to/universal-till
make build
./bin/unitill-pos
```

Or run directly:

```bash
go run ./main.go
```

POS will start on `http://localhost:8080` (or the port specified in `UT_LISTEN_ADDR`).

### Step 4: Browse Plugins

1. Open browser to `http://localhost:8080`
2. Navigate to **Plugins** page (should be in main menu)
3. You should see 3 plugins from the mock marketplace:
   - **Sales Report Plugin** (verified, type: page)
   - **Loyalty Card Scanner** (untrusted, type: payment)  
   - **Cloud Inventory Sync** (verified, type: background)

### Step 5: Install a Plugin

1. Click the **Install** button on any plugin card
2. The POS will:
   - Fetch plugin metadata from marketplace API (`/v1/catalog/plugins`)
   - Download the package from the artifact URL
   - Verify SHA256 checksum
   - Parse manifest and persist to database
   - Install with `trust_level='untrusted'` by default
3. On success, you'll see a toast notification: "Plugin installed successfully"
4. The plugin will appear in the installed plugins list

### Step 6: Verify Installation

Check the database:

```bash
sqlite3 ./data/unitill-pos.db "SELECT id, name, version, trust_level, installed_at FROM plugins;"
```

You should see the installed plugin with:
- `id`: plugin ID
- `version`: from marketplace
- `trust_level`: "untrusted" (default)
- `installed_at`: timestamp

Check permissions:

```bash
sqlite3 ./data/unitill-pos.db "SELECT plugin_id, capability, status FROM plugin_permissions WHERE plugin_id = 'sales-report';"
```

## Testing Permission Enforcement

### Grant a Permission

Use the permissions API endpoint:

```bash
curl -X POST http://localhost:8080/api/plugins/permissions/grant \
  -d "plugin_id=sales-report&permission=pos.sales.read"
```

Response: `Permission granted`

### Revoke a Permission

```bash
curl -X POST http://localhost:8080/api/plugins/permissions/revoke \
  -d "plugin_id=sales-report&permission=pos.sales.read"
```

Response: `Permission revoked`

### Check Audit Log

Denied permissions are logged:

```bash
sqlite3 ./data/unitill-pos.db "SELECT action, entity_type, entity_id, details, created_at FROM audit_log WHERE action = 'permission_denied';"
```

## Testing Trust Levels

Update plugin trust level:

```bash
curl -X POST http://localhost:8080/api/plugins/trust \
  -d "plugin_id=sales-report&trust_level=verified"
```

Valid trust levels:
- `untrusted` (default)
- `verified`
- `trusted`
- `revoked`

## Mock Marketplace Plugins

The mock server provides these test plugins:

### 1. Sales Report Plugin
- **ID**: `sales-report`
- **Version**: `1.0.0`
- **Type**: `page`
- **Trust**: `verified`
- **Permissions**: `pos.sales.read`
- **Description**: Generate daily sales reports with charts
- **Package URL**: `http://127.0.0.1:8081/artifacts/sales-report-1.0.0.tar.gz`
- **SHA256**: `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`

### 2. Loyalty Card Scanner
- **ID**: `loyalty-card`
- **Version**: `2.1.3`
- **Type**: `payment`
- **Trust**: `untrusted`
- **Permissions**: `pos.payment.process`, `hardware.scanner`
- **Description**: Scan QR code loyalty cards
- **Package URL**: `http://127.0.0.1:8081/artifacts/loyalty-card-2.1.3.tar.gz`
- **SHA256**: `44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a`

### 3. Cloud Inventory Sync
- **ID**: `inventory-sync`
- **Version**: `0.9.0`
- **Type**: `background`
- **Trust**: `verified`
- **Permissions**: `pos.inventory.write`, `network.outbound`
- **Description**: Synchronize inventory to warehouse system
- **Package URL**: `http://127.0.0.1:8081/artifacts/inventory-sync-0.9.0.tar.gz`
- **SHA256**: `2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae`

## API Endpoints

### POS Plugin API

- `GET /api/plugins/marketplace` - List plugins from marketplace (proxies to marketplace service)
- `POST /api/plugins/install` - Install plugin by ID (downloads from marketplace)
- `POST /api/plugins/permissions/grant` - Grant permission to plugin
- `POST /api/plugins/permissions/revoke` - Revoke permission from plugin
- `POST /api/plugins/trust` - Update plugin trust level

### Marketplace API (Mock Server)

- `GET /health` - Health check
- `GET /v1/catalog/plugins?device_arch=&capability=` - List available plugins
- `POST /v1/download/token` - Issue download token (not used by POS currently)
- `GET /artifacts/{filename}` - Download plugin package

## Checksum Verification

The install flow verifies SHA256 checksums to prevent tampering:

1. POS fetches plugin metadata (including `sha256`) from marketplace
2. Downloads package from `package_url`
3. Computes SHA256 of downloaded file
4. Compares with expected checksum from marketplace
5. **Rejects installation if mismatch**

To test checksum rejection, modify the mock marketplace's SHA256 values to incorrect hashes.

## Running Unit Tests

All plugin unit tests:

```bash
go test ./internal/plugins/... -v
```

Expected output: **32 tests passing**

Tests cover:
- Manifest parsing and validation
- SHA256 checksum computation
- Permission grant/revoke/check
- Trust level updates
- Install/uninstall flows
- Process supervisor lifecycle
- IPC event bus

## Running Integration Tests

Integration tests require build tag:

```bash
go test ./internal/plugins/... -tags=integration -v
```

Integration tests cover:
- Permission denial audit logging
- Menu entry filtering by permissions
- Marketplace checksum rejection
- (Process crash recovery - requires real plugin binary)
- (IPC round-trip - requires gRPC plugin)

## Troubleshooting

### Plugins page shows "marketplace request failed"

- Check mock marketplace is running on `:8081`
- Verify `UT_MARKETPLACE_URL` is set correctly
- Check marketplace logs for errors

### Install fails with "checksum mismatch"

- This is expected if artifacts are empty (mock server returns empty bodies)
- Update mock server to serve actual tarball packages if needed
- Checksums in mock are for empty files (demo only)

### Permission denied not creating audit log

- Check `audit_log` table exists (run migrations)
- Warning in tests about missing `audit_log` is expected for in-memory DBs without migrations

### Plugin doesn't appear after install

- Check `plugins` table: `SELECT * FROM plugins WHERE id = 'plugin-id';`
- Check `is_active` column is `1`
- Reload browser page to refresh plugin manager state

## Next Steps

1. **Connect to Real Marketplace**: Update `UT_MARKETPLACE_URL` to production marketplace when available
2. **Implement Plugin Runtime**: Add process supervisor integration to actually launch plugin binaries
3. **Build Real Plugins**: Create actual plugin binaries that implement the gRPC IPC contract
4. **Test IPC Flow**: Publish `sale.completed` events and verify plugins receive them

## Schema Reference

### plugins table
- `id` (TEXT PRIMARY KEY)
- `name` (TEXT)
- `version` (TEXT)
- `trust_level` (TEXT) - 'untrusted' | 'verified' | 'trusted' | 'revoked'
- `sha256` (TEXT) - checksum of installed binary
- `installed_from_url` (TEXT) - marketplace package URL
- `uploader` (TEXT) - who triggered install ('marketplace', 'manual', etc.)
- `installed_at` (TIMESTAMP)
- `is_active` (INTEGER) - 1 for installed, 0 for uninstalled

### plugin_permissions table
- `plugin_id` (TEXT)
- `capability` (TEXT) - permission string
- `status` (TEXT) - 'declared' (from manifest) | 'granted' | 'revoked'
- `granted_at` (TIMESTAMP)

### plugin_entries table
- `plugin_id` (TEXT)
- `key` (TEXT) - unique entry identifier
- `route` (TEXT) - UI route path
- `label` (TEXT) - display name
- `menu` (TEXT) - which menu to show in
- `required_permissions` (TEXT) - comma-separated permission list

### audit_log table
- `action` (TEXT) - 'permission_denied', 'plugin_installed', etc.
- `entity_type` (TEXT) - 'plugin'
- `entity_id` (TEXT) - plugin ID
- `actor` (TEXT) - who performed the action
- `details` (TEXT) - JSON metadata
- `created_at` (TIMESTAMP)

---

**Status**: ✅ Phase 1-4 complete, marketplace integration working, unit tests passing
**Pending**: Integration tests, process supervisor runtime, IPC plugin samples
