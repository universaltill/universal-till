# Tasks: Plugin Host Foundations (002f)

## Phase 1: Manifest & Persistence
- [X] PH-101 Implement manifest parser and persistence into plugin tables; include entries/settings/hooks/permissions.
- [X] PH-102 Verify SHA256 checksum at install; record provenance (source, uploader, checksum, installed_at); default `trust_level='untrusted'`.

## Phase 2: Permissions & UI
- [X] PH-201 Implement runtime capability enforcement; return clear errors and audit on deny.
- [X] PH-202 Render plugin entries only when permitted; handler tests for allow/deny cases.

## Phase 3: Process Isolation & IPC
- [X] PH-301 Add plugin process launcher/supervisor with healthcheck/restart policy; audit lifecycle events.
- [X] PH-302 Define minimal IPC/gRPC contract; implement stub handling `sale.completed` ack; integration test round-trip.

## Phase 4: Marketplace Flow
- [X] PH-401 Implement marketplace listing UI/API for binaries per OS/arch; install selected binary with checksum verification and trust toggle.

## Phase 5: Tests & Docs
- [X] PH-501 Unit tests: manifest persistence, permission enforcement.
- [X] PH-502 Integration tests: deny/audit, UI rendering permissions, process crash/restart integrity, IPC stub, marketplace checksum fail/success.
- [X] PH-503 Update contracts doc (ipc/permissions/marketplace) and quickstart notes for plugin install.
