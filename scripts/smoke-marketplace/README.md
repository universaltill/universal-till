# Marketplace Smoke Test

End-to-end smoke test for the cloud marketplace integration (feature 009).

## What It Tests

This script validates the complete plugin lifecycle through the marketplace:

1. **Health Check** - POS service availability
2. **Mock Marketplace Connectivity** - Verifies mock marketplace is running
3. **Catalog Browsing** - Fetches and parses plugin catalog (**with performance check: <3s threshold**)
4. **Marketplace Install** - Downloads and installs plugin from marketplace
5. **Installation Verification** - Validates `install_state`, `installed_from_url`, `trust_level`
6. **Enable/Disable** - Toggles plugin activation state
7. **Update Detection** - Checks for newer versions
8. **Plugin Update** - Downloads and installs update (if available)
9. **Version History** - Retrieves rollback-capable version list
10. **Rollback** - Reverts to previous version (if applicable)
11. **Revocation Endpoint** - Confirms revocation feed accessibility

### Performance Validation

The smoke test includes a **performance threshold check** for catalog rendering:

- **Target**: Catalog browsing completes in <3000ms (p90 threshold)
- **Measurement**: Includes HTTP request + JSON parsing + template rendering
- **Exclusion**: Network WAN latency (testing against localhost mock)
- **Failure**: Test fails if threshold exceeded

This validates T038 performance instrumentation requirements.

## Prerequisites

1. **Mock Marketplace** must be running on port 8082:
   ```bash
   go run scripts/mock-marketplace/main.go
   ```

2. **POS Service** must be running on port 8080:
   ```bash
   ./bin/unitill-pos
   # OR
   go run ./...
   ```

3. **Environment Variables** (in `pos.env` or `pos.env.dev`):
   ```bash
   UT_MARKETPLACE_ENDPOINT_URL=http://localhost:8082
   UT_DEV_MODE=true
   ```

## Running the Test

```bash
# From repo root
go run scripts/smoke-marketplace/main.go
```

### Expected Output

```
ℹ️  Starting marketplace smoke test...
ℹ️  Step 1: Health check
✅ Health check passed
ℹ️  Step 2: Verifying mock marketplace at http://localhost:8082
✅ Mock marketplace responding
ℹ️  Step 3: Browsing plugin catalog
✅ Catalog contains 3 plugins
ℹ️  Found test-plugin v1.0.0 in catalog
ℹ️  Step 4: Installing test-plugin v1.0.0 from marketplace
✅ Plugin installed successfully
ℹ️  Step 5: Verifying plugin installation
✅ Plugin installed_from_url: http://localhost:8082/plugins/test-plugin/1.0.0/test-plugin.tar.gz
✅ Plugin trust_level: verified
ℹ️  Step 6: Enabling plugin
✅ Plugin enabled
ℹ️  Step 7: Checking for updates
ℹ️  Available updates: 1
ℹ️  Step 8: Updating plugin to newer version
✅ Plugin updated
ℹ️  Step 9: Checking version history
✅ Version history contains 2 versions
ℹ️  Step 10: Rolling back to version 1.0.0
✅ Rollback to v1.0.0 successful
ℹ️  Step 11: Disabling plugin
✅ Plugin disabled
ℹ️  Step 12: Checking revocation sync capability
✅ Revocation endpoint responsive
ℹ️  Step 13: Testing manual plugin import
✅ Manual import endpoint available (detailed test requires artifact)

✅ 🎉 All marketplace smoke tests passed!
ℹ️  Validated flows:
ℹ️    - Catalog browsing
ℹ️    - Marketplace install with URL tracking
ℹ️    - Plugin enable/disable
ℹ️    - Plugin update detection and execution
ℹ️    - Version history tracking
ℹ️    - Rollback to previous version
ℹ️    - Revocation endpoint connectivity
```

## Failure Modes

### Mock Marketplace Not Running
```
❌ FAIL: mock marketplace unavailable: Get "http://localhost:8082/v1/catalog": dial tcp [::1]:8082: connect: connection refused
Please start it with: go run scripts/mock-marketplace/main.go
```

**Fix**: Start the mock marketplace in a separate terminal.

### POS Service Not Running
```
❌ FAIL: health check failed: Get "http://localhost:8080/api/health": dial tcp [::1]:8080: connect: connection refused
```

**Fix**: Start the POS service with `./bin/unitill-pos` or `go run ./...`.

### test-plugin Not in Catalog
```
❌ FAIL: test-plugin not found in catalog
```

**Fix**: Verify mock marketplace is serving the test plugin. Check `scripts/mock-marketplace/main.go` catalog definition.

### Installation Failed
```
❌ FAIL: install failed: status 500: Internal Server Error
```

**Fix**: Check POS logs for download errors, disk space issues, or manifest validation failures.

## Integration with Quickstart

This smoke test validates the flows documented in `specs/009-cloud-marketplace/quickstart.md`:

- **Validation Flow 4**: Plugin install from marketplace (install_state, trust_level, URL tracking)
- **Validation Flow 6**: Plugin update and rollback lifecycle
- **Validation Flow 7**: Revocation sync capability

## Adding to CI

```yaml
# Example GitHub Actions workflow
- name: Start Mock Marketplace
  run: go run scripts/mock-marketplace/main.go &
  
- name: Start POS Service
  run: ./bin/unitill-pos &
  env:
    UT_MARKETPLACE_ENDPOINT_URL: http://localhost:8082
    UT_DEV_MODE: true
  
- name: Wait for Services
  run: sleep 5
  
- name: Run Marketplace Smoke Test
  run: go run scripts/smoke-marketplace/main.go
```

## Manual Testing Extensions

To manually test additional scenarios not covered by automation:

### Manual Import
1. Create a plugin archive (`.tar.gz` or `.zip`)
2. Open `/plugins` in browser
3. Click "Import from File"
4. Upload archive
5. Verify `installed_from_url` starts with `file://`

### Revocation
1. Modify mock marketplace to mark `test-plugin` as revoked
2. Wait for revocation sync (30 min interval) or restart POS
3. Verify plugin `install_state` changes to `revoked`
4. Confirm plugin is stopped and disabled

### Telemetry
1. Enable telemetry: `UT_MARKETPLACE_TELEMETRY_OPT_IN=true`
2. Perform install/update operations
3. Check mock marketplace telemetry endpoint for received events
4. Verify batching (50 events or 5 min interval)

## Troubleshooting

**Database Locked Errors**:
- Stop POS service before running tests
- Or use separate test database: `UT_STORE=sqlite UT_DB_PATH=./data/test.db`

**Port Conflicts**:
- Ensure ports 8080 (POS) and 8082 (mock marketplace) are available
- Use `lsof -i :8080` and `lsof -i :8082` to check

**Plugin Persistence**:
- Tests may leave `data/plugins/test-plugin` directory
- Clean up between runs: `rm -rf data/plugins/test-plugin`

**Version Conflicts**:
- If rollback fails, ensure version history exists: `data/plugins/test-plugin/versions/`
- Check migration applied: `SELECT * FROM plugins WHERE id='test-plugin';`
