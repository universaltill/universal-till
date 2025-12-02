# Plugin Host Foundations (002f)

Status: Draft
Principles: offline-first; predictable plugin contracts; no schema changes

## Purpose & Goals
- Persist plugin manifests and permissions; enforce capabilities at runtime.
- Provide minimal UI rendering for plugin entries (page/button/popup/customer_facing/receipt_template).
- Support process isolation/IPC contract and audit plugin lifecycle events.

## Scope
- Manifest ingest into existing plugin tables (`plugin_catalog`, `plugins`, `plugin_entries`, `plugin_settings`, `plugin_hooks`, `plugin_permissions`).
- Runtime permission/capability enforcement and audit logging for enable/disable/install.
- Process isolation and IPC/gRPC contract stub for event dispatch.
- Marketplace install flow (list binaries per OS/arch, checksum verification, trust level default).

## Non-Goals
- Core sales/inventory/payment logic (covered elsewhere).

## Functional Requirements
- Manifest install verifies SHA256 checksum, records provenance, sets `trust_level='untrusted'` by default.
- Runtime checks enforce declared capabilities; denied actions return 403/clear errors and emit audit entries.
- Plugin UI entries render only when permitted; hidden otherwise.
- Plugin processes run isolated with start/stop/restart policy; IPC contract handles at least `sale.completed` ack path.
- Marketplace install flow lists OS/arch binaries and installs selected binary with checksum verification.

## Acceptance Criteria
- Manifest ingest persists metadata and entries/settings/hooks/permissions correctly; tests cover success/failure.
- Permission enforcement tested for allow/deny with audit logging on deny.
- Marketplace install flow verifies checksum and sets default trust; UI shows trust/approve action.
- Process isolation test proves crash/restart without DB corruption; IPC stub round-trip for `sale.completed`.
