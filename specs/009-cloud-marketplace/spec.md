# Feature Specification: Cloud Marketplace Integration (POS Client)

**Feature Branch**: `009-cloud-marketplace`  
**Created**: 2025-12-03  
**Status**: Draft  
**Input**: User description: "it will be a market place which is implementing in another repo (https://github.com/universaltill/ut-market-place) and will deploy to the cloud. However, we can run it locally and connect to it and use it locally"

**Scope Note**: The cloud marketplace (plugin store) is implementing and will be hosted in a separate repository/service. This spec covers the Universal Till POS client work needed to browse the remote catalog, fetch metadata, download plugin artifacts, install/uninstall/update them locally, and keep operating offline. No marketplace authoring, billing, or cloud hosting code lives in this repo.

## Clarifications

### Session 2025-12-03
- Q: How should FR-016 negotiate marketplace API versions between the POS client and the gRPC marketplace service? → A: Version is pinned per device via configuration (e.g., `marketplace.api_version`), and the POS sends that value in gRPC metadata so the server routes to the matching service version without relying on HTTP headers.
- Q: What authentication mechanism should the POS use to obtain marketplace access tokens? → A: Use an OAuth2 client-credentials flow per store/device fleet so the POS requests scoped tokens as itself; rotate credentials centrally and avoid embedding operator passwords.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Operator Browses Remote Catalog (Priority: P1)
Store operators need to discover plugins from the cloud marketplace directly inside the POS UI, with filters for capability, device compatibility, and trust level.

**Why this priority**: Discovery is the entry point for any plugin adoption; without a responsive catalog view the marketplace integration brings no value.

**Independent Test**: Point a POS device at the staging marketplace endpoint, open the catalog page, apply filters (e.g., "payments", Raspberry Pi), and confirm the list renders with pagination and cached fallback when the network is slow.

**Acceptance Scenarios**:
1. **Given** a POS device with internet access, **When** an operator opens "Plugin Store", **Then** the UI fetches catalog JSON from the cloud endpoint, caches it locally, and renders cards with name, rating, version, and permissions.
2. **Given** the marketplace API temporarily fails, **When** the operator reopens the catalog, **Then** the POS shows the last cached snapshot (marked "stale") and retries in the background.

---

### User Story 2 - Operator Installs Plugin from Cloud (Priority: P1)
Operators must be able to download a plugin package from the marketplace and install it on the local POS device, including manifest verification and progress feedback.

**Why this priority**: Installation delivers tangible functionality; it must be safe, resumable, and auditable.

**Independent Test**: Trigger an install for a 50 MB plugin, interrupt the network mid-download, resume, and confirm the manifest checksum matches before the host registers the plugin.

**Acceptance Scenarios**:
1. **Given** a plugin that targets the device OS/arch, **When** the operator clicks "Install", **Then** the POS requests a short-lived token, downloads the package with resume support, validates checksum/signature, and registers the plugin in `plugin_catalog` and `plugins` tables.
2. **Given** the checksum does not match, **When** validation runs, **Then** installation aborts, the package is deleted, an error toast is shown, and an audit log entry records the failure.

---

### User Story 3 - Operator Manages Installed Plugins (Priority: P1)
Users must list installed plugins, update to the latest release from the marketplace, disable/uninstall ones they no longer trust, and roll back to a previous cached version if needed.

**Why this priority**: Lifecycle management protects devices and lets stores recover quickly if a release misbehaves.

**Independent Test**: Install version 1.2, later update to 1.3, then roll back using the cached 1.2 artifact while offline.

**Acceptance Scenarios**:
1. **Given** an installed plugin with an available update, **When** the operator chooses "Update", **Then** the POS fetches differential metadata, downloads the new artifact, validates it, migrates local settings, and restarts the plugin process.
2. **Given** a plugin is disabled, **When** the POS syncs with the marketplace, **Then** it sends status telemetry so the cloud dashboard matches the device state (without uploading sensitive data).

---

### User Story 4 - Offline Continuity & Manual Imports (Priority: P2)
If the cloud marketplace is unreachable, operators still need to install plugins via USB/QR or keep using previously downloaded packages.

**Why this priority**: Local-first resilience is a core constitutional principle; cloud outages cannot block store operations.

**Independent Test**: Disconnect network, import a plugin package via USB, confirm the POS validates and installs it, and ensure the next online sync reports the install to the marketplace.

**Acceptance Scenarios**:
1. **Given** no internet connectivity, **When** an operator selects "Install from file" and provides a package exported from the marketplace, **Then** the POS validates and installs it without contacting the cloud.
2. **Given** the device was offline during a cloud revocation, **When** it reconnects, **Then** the POS fetches revocation metadata, disables affected plugins, and surfaces an alert.

---

### User Story 5 - Admin Controls & Permissions (Priority: P3)
Store admins must limit who can install/disable plugins and view audit history of marketplace actions.

**Why this priority**: Prevents accidental or malicious installs on shared tills and aligns with plugin trust policies.

**Independent Test**: Attempt plugin installs as cashier (should require manager PIN), then review audit log entries as admin.

**Acceptance Scenarios**:
1. **Given** a cashier without plugin permissions, **When** they try to install a plugin, **Then** the POS prompts for manager override and blocks the install if not approved.
2. **Given** an admin opens the audit screen, **When** they filter by "Marketplace", **Then** they see install/update/remove events with timestamps, operator IDs, and manifest hashes.

---

### User Story 6 - Dev Mode Local Marketplace Override (Priority: P2)
Developers and QA need to point the POS marketplace client at a local/stub marketplace service while in dev mode, and toggle back to the cloud endpoint quickly.

**Why this priority**: Enables local iteration and compatibility testing without touching production config; speeds up validation of marketplace changes.

**Independent Test**: Enable dev mode, set a local marketplace base URL, confirm catalog/install requests hit the local endpoint; clear/toggle to cloud and see immediate switch.

**Acceptance Scenarios**:
1. **Given** dev mode is enabled and a valid local URL is configured, **When** the catalog loads, **Then** all marketplace requests use that URL until the override is cleared or dev mode is off.
2. **Given** both local and cloud endpoints are configured, **When** the user toggles to cloud, **Then** subsequent marketplace requests use the cloud endpoint and log the switch; toggling back to local re-applies after a quick health check.
3. **Given** the local URL is malformed or unreachable, **When** the developer saves settings, **Then** the UI blocks activation with the exact error and keeps the previous working endpoint active.
4. **Given** the local endpoint dies mid-session, **When** marketplace calls fail for longer than the timeout, **Then** the POS surfaces a non-blocking warning and temporarily falls back to cloud while logging the failover.

### Edge Cases
- Marketplace API returns a feed without the device’s architecture → UI must hide the install button and explain incompatibility.
- Download token expires mid-transfer → client requests a fresh token transparently up to N retries before failing gracefully.
- Local disk lacks space for the package → installer aborts with actionable guidance and does not corrupt existing plugins.
- Revocation notice arrives for a plugin currently running critical hooks → POS must disable it safely after current transaction completes, log the action, and prompt operator.
- Dev-mode override is provided while dev mode is off → must be ignored with a warning and no endpoint switch.
- Non-HTTP(S) schemes or malformed URLs are rejected with actionable errors; self-signed certs in dev mode must be called out.
- Multiple developers on the same device keep independent overrides; clearing override reverts to the default cloud endpoint without restart.

## Requirements *(mandatory)*

### Functional Requirements
- **FR-001**: POS MUST fetch plugin catalog data from the cloud marketplace API (JSON) with pagination, filtering, and locale-aware descriptions.
- **FR-002**: POS MUST cache the latest catalog snapshot on disk (timestamped) and serve it when the network is unavailable, marking stale data to the user.
- **FR-003**: POS MUST obtain short-lived download tokens from the marketplace and use them for artifact downloads without storing operator credentials on the device.
- **FR-004**: POS MUST support resumable, checksum-verified downloads (HTTPS) and fail closed if validation or signature verification fails.
- **FR-005**: POS MUST register installations by persisting manifest metadata into `plugin_catalog`, `plugins`, `plugin_entries`, `plugin_permissions`, and `plugin_settings` using existing schema rules (no migrations).
- **FR-006**: POS MUST provide UI + API flows to update, disable, uninstall, or roll back plugins, ensuring related DB rows and files are cleaned up atomically.
- **FR-007**: POS MUST surface marketplace revocation notices pulled from the cloud (delta feed) and automatically disable revoked plugins within 15 minutes of sync.
- **FR-008**: POS MUST log every marketplace interaction (browse, install, update, revoke, manual import) to `audit_log` with actor info and manifest hash.
- **FR-009**: POS MUST expose role-based access control so only managers/admins can install or remove plugins; overrides require authentication.
- **FR-010**: POS MUST support manual package import/export (USB/file picker) that follows the same manifest verification path used for cloud downloads.
- **FR-011**: POS MUST detect compatibility (OS, arch, required capabilities) before enabling the "Install" action by comparing catalog metadata against the device profile; incompatible plugins stay disabled in the UI/API with a clear explanation banner.
- **FR-012**: POS MUST backoff and retry marketplace syncs using exponential strategy to avoid hammering the remote API during outages.
- **FR-013**: POS MUST send lightweight telemetry (plugin id, version, status) to the marketplace when online only if `settings.marketplace.telemetry_opt_in` is true, and must avoid sensitive data when reporting.
- **FR-014**: POS MUST maintain at least one previous version per plugin (if disk permits) to allow rollback when an update misbehaves.
- **FR-015**: POS MUST expose configuration for marketplace endpoint URLs (prod/staging/custom) so partner deployments can point to their own cloud instance.
- **FR-016**: POS MUST gracefully handle marketplace API version mismatches by pinning the target gRPC service version via device configuration and attaching it as metadata on every request; devices must log and alert when the configured version is deprecated.
- **FR-017**: POS MUST recognize and enforce the canonical plugin types (`page`, `button`, `popup`, `payment`, `device`, `integration`, `report`, `pricing`, `tax`, `import`, `export`, `hardware`, `background_job`, `scheduler`, `receipt_template`, `customer_facing`, `auth`, `notification`, `delivery`) when rendering catalog entries, validating manifests, and applying permissions.
- **FR-018**: POS MUST authenticate to the marketplace using an OAuth2 client-credentials flow (store/device client ID + secret), request scoped tokens, cache them securely, and rotate credentials without operator interaction.
- **FR-019**: POS MUST auto-start every installed + enabled plugin as a supervised executable at POS startup, ensuring each process exposes the gRPC contract defined in `contracts/plugin_host.proto` so host↔plugin IPC stays consistent.
- **FR-020**: POS MUST support a dev-mode-only marketplace base URL override that, when enabled, routes all marketplace calls to the configured local URL; override is ignored when dev mode is off.
- **FR-021**: POS MUST validate the override (scheme/host/port) and reachability before activation, surface human-readable errors, and keep the previous endpoint active on failure.
- **FR-022**: POS MUST support runtime toggling between cloud and local endpoints without restart, log the active source with timestamp, and fall back to cloud automatically after a configurable timeout if the local endpoint stops responding.

### Key Entities *(include if feature involves data)*
- **MarketplaceFeed**: Cached JSON snapshot (version, timestamp, locale, list of PluginSummaries) fetched from the cloud API.
- **PluginSummary**: Subset of remote metadata (id, name, version, type from the canonical list, capabilities, trust_level, pricing flag) displayed in the UI catalog.
- **DownloadRequest**: Tracks pending download token, artifact URL, progress, checksum, retries, and expiry.
- **LocalPlugin**: Represents an installed plugin on the device (manifest data including canonical type, file path, trust state, installed_at, last_sync_at, cached_versions).
- **RevocationNotice**: Message from cloud containing plugin id, version, reason, action (disable/delete) that the POS must enforce.
- **AuditEvent**: Existing table entries enriched with marketplace context (action, actor_id, plugin_id, manifest_hash, source="cloud"/"manual").

## Success Criteria *(mandatory)*

### Measurable Outcomes
- **SC-001**: Catalog view fetch + render completes in <3 seconds on 90% of requests when online (excluding remote latency) and uses cached data when offline.
- **SC-002**: 95% of plugin downloads either complete successfully or resume after transient failures without user intervention; checksum mismatches are detected 100% of the time.
- **SC-003**: Revocation feeds are processed within 15 minutes for 95% of online devices, automatically disabling affected plugins and logging the action.
- **SC-004**: At least one previous plugin version remains available locally for rollback on 90%+ of update attempts (subject to disk space); rollback completes in <2 minutes.
- **SC-005**: Audit log entries exist for 100% of install/update/uninstall/revoke/manual-import actions initiated through the marketplace UI or API.
- **SC-006**: A developer can switch marketplace endpoints (local ↔ cloud) and observe requests hitting the new endpoint within 60 seconds, with health check and logging.
- **SC-007**: At least 90% of malformed/failed local overrides produce actionable validation errors; fallback to cloud occurs within 5 seconds of detecting an unresponsive local endpoint.

## Assumptions & Constraints
- Cloud marketplace provides stable REST endpoints for catalog, download token issuance, telemetry, and revocation feeds; this repo only consumes them.
- No schema changes are allowed; all persistence uses existing plugin tables defined in `internal/db/migrations/001_init.sql`.
- POS devices may run offline for extended periods; features must degrade gracefully and reconcile once connectivity returns.
- Marketplace authentication uses OAuth2 client-credentials per store/device; secrets are provisioned centrally and rotated without exposing operator accounts.
- Telemetry reporting honors the persisted opt-in flag `settings.marketplace.telemetry_opt_in` and remains disabled when false.
- Dev-mode gating (`UT_DEV_MODE=true`) is the only context where local overrides apply; production/staging configs must remain unaffected by overrides.
