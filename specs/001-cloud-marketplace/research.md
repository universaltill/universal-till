# Phase 0 Research — Cloud Marketplace Integration (POS Client)

## Decision Records

### 1. Marketplace API Version Negotiation
- **Decision**: Pin the marketplace gRPC/API version per device via `marketplace.api_version` setting and send it as gRPC metadata on every call.
- **Rationale**: Keeps URLs stable, enables gradual rollout/rollback by configuration, and works for both HTTP and gRPC transports without custom Accept headers.
- **Alternatives Considered**: (a) `Accept` header negotiation—does not apply to gRPC metadata cleanly; (b) query-string `api_version`—fragile and couples clients to URL formats.

### 2. Authentication Flow
- **Decision**: Use OAuth2 client-credentials (store/device scoped) to obtain short-lived access tokens; secrets live in encrypted settings and rotate centrally.
- **Rationale**: Avoids embedding operator credentials, enables unattended installs, and aligns with Universal Till account system.
- **Alternatives Considered**: (a) Device-code flow—too much UX friction; (b) static API keys—hard to rotate and less auditable.

### 3. Artifact Download & Resume Strategy
- **Decision**: Download artifacts over HTTPS with Range requests into `.part` files under `data/plugins/tmp/`, resume using ETag+size validation, and only promote to final path after checksum/signature verification.
- **Rationale**: Works with large binaries (50–200 MB) on slow links, avoids corrupt installs, and leverages existing filesystem.
- **Alternatives Considered**: (a) Chunked uploads via custom protocol—overkill; (b) streaming directly into SQLite BLOB—bloats DB and complicates rollback.

### 4. Plugin Executable Lifecycle & IPC
- **Decision**: Treat every plugin as a local executable (preferably Go) started by the POS at boot; supervise via `internal/plugins` manager that launches processes with per-plugin UNIX sockets / TCP loopback gRPC endpoints.
- **Rationale**: Matches existing host expectations, isolates crashes, and keeps IPC uniform (gRPC) regardless of plugin type (page/payment/device/etc.).
- **Alternatives Considered**: (a) In-process plugins—risk crashing host; (b) HTTP callbacks—harder to secure on localhost and adds another stack.

### 5. Canonical Plugin Types & Manifest Alignment
- **Decision**: Enforce the canonical type list shared with `specs/002f-plugin-host` (page, button, popup, payment, device, hardware, integration, delivery, pricing, tax, report, import, export, background_job, scheduler, customer_facing, receipt_template, auth, notification) and persist type in `plugin_entries` + manifest cache for UI routing.
- **Rationale**: Keeps marketplace catalog, manifest ingest, and runtime enforcement consistent; avoids duplicate type enumerations drifting between specs.
- **Alternatives Considered**: (a) Free-form string types—leads to inconsistent UI; (b) per-plugin custom capabilities—already covered via permissions table.

### 6. Revocation & Telemetry Sync Pattern
- **Decision**: Use a background job (server ticker) that every 5 minutes pulls revocation deltas, applies changes within 15 minutes, and pushes lightweight telemetry (plugin id/version/status) when online.
- **Rationale**: Meets success criteria, keeps load predictable, and leverages existing background runner.
- **Alternatives Considered**: (a) Push-based webhooks—devices are often offline; (b) real-time polling per action—wastes bandwidth.

## Outstanding Notes
- Need sample proto definitions for marketplace catalog/download/token services (captured in Phase 1 contracts).
- Plugin sandboxing remains OS-level (user permissions + process isolation); no additional kernel features required for this iteration.
