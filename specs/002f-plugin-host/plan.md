# Plan: Plugin Host Foundations (002f)

Branch: `002-pos-core-mvp` | Inputs: specs/002f-plugin-host/spec.md

## Summary
Implement plugin manifest persistence, runtime permission enforcement, UI entry rendering, process isolation + IPC contract, and marketplace install flow using existing plugin tables. No schema changes; default trust level untrusted.

## Phases
1) Manifest & Persistence
- Parse manifest fields; persist to `plugin_catalog`, `plugins`, `plugin_entries`, `plugin_settings`, `plugin_hooks`, `plugin_permissions`.
- Verify SHA256 checksum and record provenance fields.

2) Permissions & UI
- Implement runtime capability checks; return clear errors and audit on deny.
- Render plugin entries (page/button/popup/customer_facing/receipt_template) only when permitted.

3) Process Isolation & IPC
- Add process launcher/supervisor with timeouts/healthchecks; record lifecycle audit events.
- Define minimal IPC/gRPC contract and stub; support `sale.completed` ack in tests.

4) Marketplace Install Flow
- List available binaries per OS/arch; install selected binary with checksum verify; set `trust_level='untrusted'` by default; expose trust/approve action.

5) Tests
- Unit tests for manifest parsing/persistence and permission enforcement.
- Integration tests for deny/audit, UI rendering permissions, process crash/restart, IPC stub, marketplace install checksum failure/success.

## Constraints
- No schema changes; predictable plugin contracts; offline-capable operations.

## Deliverables
- Manifest ingest pipeline, runtime permission checker, UI rendering controls.
- Process supervisor + IPC stub, marketplace install flow.
- Tests and doc notes (contracts/quickstart) as needed.
