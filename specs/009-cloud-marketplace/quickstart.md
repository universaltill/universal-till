# Quickstart — Cloud Marketplace Integration

## Prerequisites
- Go 1.25, make, sqlite3 CLI (optional for inspection).
- Marketplace staging credentials (OAuth2 client ID/secret) and endpoint URLs.
- At least 500 MB free disk under `data/plugins/` for artifacts + rollback copy.

## 1. Configure Environment
```bash
cp pos.env.example pos.env.dev
export $(grep -v '^#' pos.env.dev | xargs) # or use direnv
cat >> pos.env.dev <<'EOF'
UT_MARKETPLACE_ENDPOINT="https://staging.marketplace.example.com"
UT_MARKETPLACE_GRPC="staging.marketplace.example.com:443"
UT_MARKETPLACE_CLIENT_ID="store-123-pos"
UT_MARKETPLACE_CLIENT_SECRET="<redacted>"
UT_MARKETPLACE_API_VERSION="1"
UT_MARKETPLACE_DEVICE_ID="pos-mac-mini-01"
UT_MARKETPLACE_TELEMETRY_OPT_IN="false" # flip to true when ready to report
EOF
```

## 2. Build & Run POS
```bash
make build
UT_STORE=sqlite ./bin/unitill-pos --config pos.env.dev
```
The server launches on `http://localhost:8080` with HTMX UI.

## 3. Seed Plugin Catalog Snapshot (optional offline test)
```bash
curl -H "Authorization: Bearer $(./bin/unitill-pos token print)" \
  "$UT_MARKETPLACE_ENDPOINT/api/v1/catalog?locale=en&arch=linux/arm64" \
  -o data/plugins/cache/prod.catalog.json
```
Restart the app to ingest the cached snapshot.

## 4. Validate Key Flows
1. **Browse Catalog**: Open `/plugins/store`, verify filters (type, capability) work online and offline (disconnect network to confirm stale badge).
2. **Install Plugin**: Choose a plugin (e.g., payment). Click *Install*, observe download progress modal, interrupt network mid-way, then resume—ensure install finishes, shows checksum toast, and entry appears under Installed.
3. **Plugin Execution**: Confirm the installed plugin binary exists under `data/plugins/<plugin_id>/<version>/` and that the POS logs show process start with gRPC endpoint.
4. **Rollback**: Update plugin to a newer version (simulate via staging feed). Use *Rollback* action; verify previous `versions/` folder restored and audit log entry created.
5. **Revocation Sync**: Trigger a revocation via staging admin. Run `curl -X POST $UT_MARKETPLACE_ENDPOINT/test/revoke ...` or wait for background job; ensure plugin disables within 15 minutes and alert banner appears.
6. **Manual Import**: Place a `.utplugin` package exported from marketplace into USB path, use *Install from File*, and ensure `source=manual` audit entry exists.
7. **Performance Check**: Run `go test ./scripts/smoke_quickstart -run TestCatalogRender` (or `go run scripts/smoke_quickstart/main.go --check-catalog`) to confirm `/plugins/store` renders <3s p90 excluding WAN latency; review logs emitted by the handler instrumentation if thresholds exceed limits.

## 5. Test Suite
```bash
UT_STORE=sqlite go test ./internal/plugins ./internal/pages ./internal/httpx -run "Plugin|Marketplace"
```
Focus tests should cover catalog sync, download manager resume, manifest ingest, RBAC, revocation handler, and plugin supervisor start/stop.

## 6. Troubleshooting
- **Catalog 401**: Verify OAuth2 credentials and time skew; tokens cached under `data/plugins/auth/token.json`.
- **Download resume fails**: Clear `.part` file in `data/plugins/tmp/` and retry; check WAN path for Range support.
- **Plugin not appearing in UI**: Ensure manifest declares canonical `type` and that `plugin_entries.is_active=1`.
- **Background sync disabled**: Confirm `settings.marketplace.sync_enabled` flag via admin UI.
- **Telemetry not sending**: Ensure `settings.marketplace.telemetry_opt_in` is true and the device has recent tokens; otherwise telemetry remains off by design.
