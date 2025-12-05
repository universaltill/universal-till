# Implementation Plan: Cloud Marketplace Integration (POS Client)

**Branch**: `009-cloud-marketplace` | **Date**: 2025-12-03 | **Spec**: `specs/009-cloud-marketplace/spec.md`
**Input**: Feature specification from `specs/009-cloud-marketplace/spec.md`

## Summary

Universal Till POS must integrate with the remotely hosted plugin marketplace: browse catalog feeds, authenticate via OAuth2 client credentials, download plugin artifacts over HTTPS/gRPC token exchanges, install/update/uninstall plugins locally, and keep working offline. Plugins remain separate executables (preferably Go) launched by the POS; depending on their declared canonical type (page, payment, device, etc.) the host wires UI/menu entries or API shims that communicate via gRPC IPC. Work includes resilient caching, compatibility gating before enabling installs, resume-capable downloads, rollback storage, revocation handling, audit logging, RBAC enforcement, marketplace API version pinning, and a dev-mode-only local marketplace URL override (validation/toggle/fallback) without changing the existing SQLite schema.

**Performance Measurement**: Instrument `/plugins/store` handlers to log render timings excluding remote latency, expose metrics in the smoke script (`scripts/smoke_quickstart`) that fail if 90th percentile exceeds 3 seconds, and document the measurement procedure in `quickstart.md`.

**Dev Override & Versioning**: Pin marketplace API version metadata on all calls (FR-016) and support a dev-mode-only local marketplace base URL override with validation, runtime toggle, logging, and fallback to cloud on failure (FR-020–FR-022).

## Technical Context

**Language/Version**: Go 1.25 (per repo toolchain) with CGO-less `modernc.org/sqlite` driver; plugins encouraged to use Go for low-spec hardware.  
**Primary Dependencies**: standard library (`net/http`, `crypto`, `os`), `google.golang.org/grpc` for marketplace + local plugin IPC, `modernc.org/sqlite`, `github.com/google/uuid`, HTMX/Alpine for UI updates, `compress/gzip` and `archive/zip` for artifact handling.

**Storage**: SQLite DB defined by `internal/db/migrations/001_init.sql` for catalog/plugins/audit, plus filesystem cache under `data/plugins/` for downloaded artifacts and previous versions.  
**Testing**: `go test ./...`, focused tests in `internal/plugins`, `internal/pages`, `internal/httpx`, plus integration tests that spin up fake marketplace gRPC servers and plugin executables; manual smoke via `make build && ./bin/unitill-pos`.  
**Target Platform**: Local-first POS on Linux/macOS (including ARM SBCs). Plugins execute on the same host, launched as child processes with gRPC endpoints.  
**Project Type**: Single Go binary serving HTML UI (htmx) and orchestrating plugin host/runtime.  
**Performance Goals**: Catalog fetch/render <3s excluding WAN latency; downloads resume successfully for ≥95% transient failures; plugin start latency <2s on Raspberry Pi-class hardware; revocation enforcement within 15 minutes of connectivity.  
**Constraints**: Offline-first operation, no schema changes, enforce canonical plugin types and permissions, executable plugins must be validated and sandboxed, limited disk (must cap cached versions), low memory footprint (<512 MB) on edge devices, network operations must backoff to protect remote marketplace, and telemetry must honor `settings.marketplace.telemetry_opt_in`.  

- Changes MUST respect the existing SQLite schema defined in `internal/db/migrations/001_init.sql` and documented in `docs/data-model.md`.
- Do not rename or drop columns/tables unless the spec explicitly calls for a migration and data migration strategy (not allowed for this feature).
  
**Scale/Scope**: Single store with 1–5 tills, 10–30 plugins installed (multiple types), catalog feeds up to several hundred entries, WAN bandwidth constrained (1–5 Mbps), plugin artifacts up to ~200 MB, and every installed plugin runs as a supervised executable exposing the gRPC contract defined in `contracts/plugin_host.proto`.

## Constitution Check

- **Correctness over cleverness**: Plan emphasizes explicit checksum/signature validation, manifest verification, and auditable state transitions. ✅
- **Stable domain model**: Reuses existing plugin tables; no migrations. ✅
- **Predictable plugin contracts**: Canonical type list enforced; gRPC IPC contracts versioned per FR-016. ✅
- **Local-first & resilient**: Offline catalog cache, manual imports, and plugin auto-start at boot keep tills functional without WAN. ✅
- **Testable core**: Domain logic isolated in `internal/plugins`, `internal/pages`, and download manager with fake marketplace servers for unit tests. ✅
- **Security & data integrity**: OAuth2 client credentials, RBAC enforcement, audit logging, and executable validation prevent tampering. ✅

Gates pass for Phase 0.

**Post-Design Re-check (Phase 1)**: Data model binds exclusively to existing plugin tables, contracts stay versioned via proto definitions, and quickstart enforces offline-first & security controls. No new risks introduced → gates remain ✅.

## Project Structure

### Documentation (this feature)

```text
specs/009-cloud-marketplace/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 mapping between spec entities and DB tables
├── quickstart.md        # Phase 1 operations + verification steps
├── contracts/           # Phase 1 API contracts (marketplace gRPC + plugin IPC)
└── tasks.md             # Generated via /speckit.tasks in Phase 2
```

### Source Code (repository root)

```text
main.go
internal/
  config/           # marketplace endpoint + OAuth credentials wiring
  db/               # SQLite helpers (unchanged schema)
  pages/            # UI handlers for plugin store, installs, audit views
  plugins/          # Plugin manager, manifest ingest, process/runtime control
  httpx/            # HTMX helpers for catalog/install modals
  settings/         # persisted marketplace + RBAC settings
  server/           # HTTP + background workers (sync loops)
pos/
  ...               # domain logic (reuse for RBAC + audit integration)
web/
  ui/pages/         # Plugin store UI, install progress, audit views
  public/           # JS/CSS for progress indicators
data/plugins/       # Downloaded artifacts, cached versions, manifests
scripts/
  smoke_quickstart/ # manual verification harness for installs/rollback
```

**Structure Decision**: Single Go backend orchestrating UI + plugin runtime; extend `internal/plugins` for executable lifecycle, `internal/pages` for HTMX store UI, and `web/ui` for templates. Background marketplace sync loops run from `server/` using existing task runners; no additional microservices required.

## Complexity Tracking

> **Fill ONLY if Constitution Check has violations that must be justified**

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| [e.g., 4th project] | [current need] | [why 3 projects insufficient] |
| [e.g., Repository pattern] | [specific problem] | [why direct DB access insufficient] |

## Additional Workstreams (Post-merge Update)

1) Dev Mode Override & Version Pinning  
- Wire marketplace API version metadata on all marketplace client calls with configurable pin/deprecation alerts (FR-016).  
- Implement a dev-mode-only marketplace override with validation (scheme/host/port/reachability), runtime toggle between local/cloud, logging/audit of active source, and fallback to cloud on timeout (FR-020–FR-022).
