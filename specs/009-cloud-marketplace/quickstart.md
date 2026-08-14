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
# Marketplace configuration (see pos.env.example for all options)
UT_MARKETPLACE_ENDPOINT_URL="https://staging.marketplace.example.com"
UT_MARKETPLACE_CLIENT_ID="store-123-pos"
UT_MARKETPLACE_CLIENT_SECRET="<redacted>"
UT_MARKETPLACE_API_VERSION="1.0.0"
UT_MARKETPLACE_TELEMETRY_OPT_IN="false" # flip to true when ready to report
# Dev mode settings (optional)
UT_DEV_MODE="false"
UT_MARKETPLACE_DEV_OVERRIDE_URL="" # local marketplace override when dev mode is true
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

## 7. Implementation Notes (Phase 1-6 Complete)

### Architecture Overview
The marketplace integration follows a **local-first** architecture where:
- Catalog snapshots are cached locally (`data/plugins/cache/`)
- Manual catalog refresh is required (no automatic first-fetch)
- All plugin operations work offline with cached data
- Background sync updates stale caches every 15 minutes

### Key Components Implemented

**Phase 1-2: Foundation**
- OAuth2 token client with secure on-disk caching (`internal/plugins/oauth/`)
- Marketplace HTTPS client with API versioning (`internal/plugins/marketplace/client.go`)
- Plugin cache storage with .part file tracking — superseded by `download_manager.go`/`installer_store.go`'s own `.part`-file handling; `internal/plugins/storage/` was dead code and removed 2026-08-14 (ut-docs#28)
- Background scheduler for catalog sync, telemetry, revocation (`internal/server/server.go`)

**Phase 3: Catalog Browsing (US1)**
- `CatalogRepository` with snapshot persistence and stale detection
- HTMX-based `/plugins/store` UI with type/capability filters
- Offline fallback with stale badge display
- Periodic sync with exponential backoff (max 3 retries)

**Phase 4: Plugin Installation (US2)**
- `DownloadManager` with HTTP Range requests for resume support
- SHA256 checksum validation during download
- Ed25519 signature verification via `ManifestVerifier`
- Atomic database persistence with transaction support
- OS/arch compatibility gating (blocks mismatched installs)
- Secure tar.gz extraction with path traversal protection

**Phase 5: Plugin Lifecycle (US3)**
- `UpdateChecker` compares installed vs catalog versions
- `RollbackManager` maintains version history (max 3 versions)
- `Supervisor.AutoStartPlugins()` launches active plugins at boot
- `TelemetryClient` batches events (50 items or 5min), honors opt-in flag
- Management UI with update badges, enable/disable, rollback actions

**Phase 6: Offline Continuity (US4)**
- `Importer` supports .zip and .tar.gz formats for manual installs
- File upload endpoint with multipart handling (max 200 MB)
- Modal UI with drag-and-drop, file validation, security warnings
- `RevocationChecker` syncs marketplace revocation feed (30min interval)
- Automatic plugin disabling when revoked, with audit trail

### File Structure
```
data/plugins/
├── cache/                    # Catalog snapshots (prod.catalog.json)
├── tmp/                      # Temporary downloads (.part files)
├── auth/                     # OAuth2 token cache
├── {plugin_id}/
│   ├── {version}/            # Current installed version
│   │   ├── manifest.json
│   │   ├── entrypoint (executable)
│   │   └── ... (plugin files)
│   └── versions/             # Rollback history (max 3)
│       ├── 1.0.0/
│       ├── 1.1.0/
│       └── 1.2.0/
```

### Database Schema Updates
- `plugins.install_state`: 'pending'|'installed'|'revoked'
- `plugins.trust_level`: 'untrusted'|'trusted'|'verified'
- `plugins.is_active`: Controls enable/disable state
- Audit log entries for install, update, rollback, revocation events

### API Endpoints
- `POST /api/plugins/install-from-marketplace` - Marketplace install
- `POST /api/plugins/import-from-file` - Manual upload (multipart)
- `POST /api/plugins/{id}/update` - Update to latest version
- `POST /api/plugins/{id}/rollback` - Revert to previous version
- `POST /api/plugins/{id}/enable` - Activate plugin
- `POST /api/plugins/{id}/disable` - Deactivate plugin
- `GET /api/plugins/check-updates` - List available updates

### Background Jobs
- **Catalog Sync** (15min): Refreshes stale snapshots from marketplace
- **Telemetry** (5min): Flushes batched events (if opt-in enabled)
- **Revocation Check** (30min): Syncs revocation feed, disables affected plugins

### Security Features
- Path traversal protection in archive extraction
- SHA256 integrity verification for all downloads
- Ed25519 signature verification for marketplace artifacts
- Trust tier mapping (verified/approved → trusted)
- Disk budget enforcement (max 1 GB per plugin)
- Untrusted default for manual imports
- Dev-mode signature bypass (controlled by UT_ENV=dev)

### Testing Strategy
Unit tests cover:
- Catalog repository offline replay
- Download manager resume logic
- Manifest verification (checksum + signature)
- Update version comparison
- Rollback transaction atomicity
- Revocation sync processing
- Telemetry opt-in enforcement

Integration tests validate:
- End-to-end install flow (marketplace → download → verify → persist)
- Update with rollback version storage
- Manual import from local file
- Revocation disabling running plugins
- Background job orchestration

### Known Limitations
- Supervisor not yet integrated into main.go (revocation won't stop processes)
- RBAC manager PIN prompts not implemented (Phase 7)
- Performance instrumentation pending (Phase 8)
- Dev-mode marketplace override pending (Phase 9)

