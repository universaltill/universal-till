# Phase 1 Data Model — Cloud Marketplace Integration

## Overview

This feature does **not** add new tables. It maps marketplace concepts onto the existing plugin schema introduced in `internal/db/migrations/001_init.sql` plus filesystem caches under `data/plugins/`. The marketplace service remains remote; the POS client persists metadata locally for offline use.

```
Marketplace API (gRPC/HTTPS)
        ↓ catalog / tokens / telemetry / revocations
POS Runtime (internal/plugins)
        ↓ persist via db.Tx helpers
SQLite (plugin_* tables, audit_log)
```

## Entity Mapping

| Spec Entity | Persistence Strategy | Notes |
|-------------|---------------------|-------|
| **MarketplaceFeed** | JSON snapshot stored under `data/plugins/cache/<endpoint>/catalog.json` with metadata row in `settings` (`marketplace.feed_timestamp`). | UI reads snapshot when offline; background sync updates file + timestamp atomically. |
| **PluginSummary** | `plugin_catalog` for immutable metadata (id, name, vendor, trust level, min_version, arch); `plugin_entries` for declared UI hooks; `plugin_permissions` for requested capabilities. | When syncing catalog, refresh rows but keep `installed_version`/local overrides in `plugins`. |
| **DownloadRequest** | Ephemeral row in `plugins` table (`download_state` JSON column) plus `.part` file in `data/plugins/tmp`. | Stores token expiry, checksum, retries; deleted once install completes. |
| **LocalPlugin** | `plugins` table (installed_version, trust_level, enabled, path), `plugin_settings` for config, `plugin_entries` for UI wiring. Cached artifacts saved in `data/plugins/<plugin_id>/<version>/`. | Each plugin folder contains executable, manifest, assets; `plugins.previous_version_path` references rollback candidate. |
| **RevocationNotice** | `audit_log` (action = `plugin.revoked`, metadata JSON), plus fields on `plugins` (`is_active=false`, `revoked_reason`). | Background sync disables plugin, logs actor `system`. |
| **AuditEvent** | Existing `audit_log` table with `action` values: `marketplace.browse`, `marketplace.install`, `marketplace.update`, `marketplace.rollback`, `marketplace.revoke`, `marketplace.manual_import`. | Store manifest hash, plugin id, operator id, source (`cloud` vs `manual`). |

## State Transitions

1. **Catalog Sync**
   - Fetch feed → write snapshot file → upsert `plugin_catalog` rows (insert new, update `latest_version`/metadata) inside a transaction.
   - Update `settings` key `marketplace.feed_checksum` for cache validation.

2. **Install / Update Flow**
   - Insert/Update `plugins` row with `status='downloading'`, persist `download_state` JSON (token, retries).
   - Stream artifact to `.part`; on checksum match, move to `data/plugins/<id>/<version>/` and update `plugins.path`, `installed_version`, `enabled` (default false until trust approved).
   - Populate `plugin_entries`, `plugin_permissions`, `plugin_settings` from manifest payload; create `audit_log` row for completion.

3. **Rollback Flow**
   - Copy previous version path info from `plugins.previous_version_path`; swap directories and update `installed_version` while logging `marketplace.rollback`.
   - Leave the newer artifact cached for optional reapply.

4. **Revocation Handling**
   - Background job marks `plugins.is_active=false`, `trust_level='revoked'`, and records `revoked_at` timestamp.
   - Emit `audit_log` entry and surface alert via `settings` flag consumed by UI.

5. **Manual Import**
   - Operator selects local package → same validation path → mark `source='manual'` in `audit_log` and set `plugins.install_source='manual'`.

## Validation & Constraints

- **Checksums/Signatures**: Store manifest hash in `plugins.manifest_hash` (existing column) and compare before enabling.
- **Canonical Types**: `plugin_entries.type` must be one of the enumerated values; validation occurs during manifest ingest.
- **RBAC**: Map operator roles to `users`/`settings`; enforce before mutating any `plugins` or `plugin_catalog` rows.
- **Disk Quotas**: Track cumulative size per plugin directory; expose `settings.marketplace.disk_budget_mb` to avoid filling disk.
- **Telemetry Opt-in**: Honor `settings.marketplace.telemetry_opt_in`; telemetry client reads the flag before enqueueing status uploads and persists operator overrides via `settings`.

## Filesystem Layout

```
data/
  plugins/
    cache/
      prod.catalog.json
    tmp/
      <plugin_id>.part
    <plugin_id>/
      manifest.json
      versions/
        1.2.0/
          plugin
          assets/
        1.3.0/
          plugin
```

## Eventing & IPC

- Plugin start metadata stored in `plugins.process_endpoint` (loopback address/Unix socket) for `internal/plugins` supervisor, and marketplace installs must populate it so the auto-start manager can launch the executable on boot per FR-019.
- `plugin_hooks` retains declared event subscriptions (e.g., `sale.completed`), enabling the host to route gRPC calls to running plugin processes.
