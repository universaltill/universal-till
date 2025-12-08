# Tasks: Cloud Marketplace Integration (POS Client)

**Input**: `specs/009-cloud-marketplace/`

## Phase 1: Setup (Shared Infrastructure)
- [X] T001 Add marketplace configuration struct + env wiring in `internal/config/config.go` and surface defaults in `pos.env.example`.
- [X] T002 [P] Create local plugin cache directories (`data/plugins/cache`, `data/plugins/tmp`) via startup bootstrap in `main.go`.
- [X] T003 [P] Document new marketplace environment keys in `specs/009-cloud-marketplace/quickstart.md` and `README.md`.

---

## Phase 2: Foundational (Blocking Prerequisites)
- [X] T004 Implement OAuth2 client-credentials token manager in `internal/plugins/oauth/token_client.go` with secure on-disk cache.
- [X] T005 [P] Add marketplace gRPC/HTTPS client abstractions in `internal/plugins/marketplace/client.go` following `contracts/marketplace.proto`.
- [X] T006 [P] Build download cache abstraction (`internal/plugins/storage/cache_store.go`) that tracks `.part` files and disk quotas.
- [X] T006b [P] Attach API version metadata to all marketplace requests (per FR-016), with config wiring, deprecation alerting, and tests.
- [X] T007 Define canonical plugin type enums + validation helpers in `internal/plugins/types.go` reused by host + UI.
- [X] T008 Wire RBAC + audit helpers in `internal/plugins/authorizer.go` so installs/updates enforce manager overrides globally.
- [X] T009 Add scheduler stubs in `internal/server/server.go` for catalog sync, telemetry, and revocation jobs (no logic yet).

**Checkpoint**: Marketplace auth, clients, storage, and RBAC scaffolding are ready; user stories can start.

---

## Phase 3: User Story 1 – Operator Browses Remote Catalog (Priority: P1)
**Goal**: Fetch marketplace catalog, cache locally, and render responsive UI with filters + offline fallback.

**Independent Test**: Point POS at staging marketplace, open `/plugins/store`, apply filters, and confirm cached snapshot appears when WAN drops (shows stale badge) while background retry runs.

### Implementation & Tests
- [X] T010 [P] [US1] Implement catalog repository with on-disk snapshot + stale markers in `internal/plugins/marketplace/catalog_repository.go`.
- [X] T011 [US1] Add periodic catalog sync job + exponential backoff in `internal/server/scheduler.go` invoking the repository.
- [X] T012 [P] [US1] Create HTMX handlers in `internal/pages/plugins_page.go` to list/filter plugins and expose stale metadata.
- [X] T013 [P] [US1] Build UI templates/partials (`web/ui/pages/plugins_store.html`, `web/ui/partials/plugins_filters.html`) with capability/device filters.
- [X] T014 [US1] Add catalog caching tests covering offline replay in `internal/plugins/marketplace/catalog_repository_test.go`.

---

## Phase 4: User Story 2 – Operator Installs Plugin from Cloud (Priority: P1)
**Goal**: Download artifacts with resume support, verify manifests, and register plugins atomically with audit + RBAC prompts.

**Independent Test**: Install a 50 MB plugin, drop network mid-download, resume, and ensure checksum + signature validate before plugin appears under Installed with audit entry.

### Implementation & Tests
- [X] T015 [P] [US2] Implement resumable download manager with Range requests + `.part` promotion in `internal/plugins/download_manager.go`.
- [X] T016 [P] [US2] Add manifest verifier in `internal/plugins/manifest_verifier.go` using data-model mappings:
  - T016a: Implement SHA256 checksum validation against marketplace-provided hash.
  - T016b: Implement cryptographic signature verification using marketplace public key (RSA or Ed25519) to prevent tampering.
- [X] T017 [US2] Extend plugin install API handler + RBAC prompts in `internal/pages/plugin_api.go` (install endpoint, manager PIN modal).
- [X] T018 [P] [US2] Create HTMX progress modal and toast partials in `web/ui/partials/plugin_install_modal.html`.
- [X] T019 [US2] Persist install metadata (plugin_entries, permissions, settings) and audit log writes in `internal/plugins/manager.go`.
- [X] T019a [US2] Enforce compatibility gating in `internal/pages/plugin_api.go` and `web/ui/pages/plugins_store.html`, disabling installs for mismatched OS/arch/capabilities and adding tests in `internal/pages/plugins_page_test.go`.
- [ ] T020 [P] [US2] Add download + manifest failure tests in `internal/plugins/download_manager_test.go` and `internal/plugins/manifest_verifier_test.go`.

---

## Phase 5: User Story 3 – Operator Manages Installed Plugins (Priority: P1)
**Goal**: Update, disable, uninstall, roll back, and auto-start plugins; send telemetry so cloud matches device state.

**Independent Test**: Update plugin 1.2→1.3, roll back to 1.2 offline, confirm cached artifacts exist and telemetry reports status without WAN errors.

### Implementation & Tests
- [X] T021 [P] [US3] Implement update checker + diff metadata handling in `internal/plugins/update_checker.go` tied to marketplace client.
- [X] T022 [US3] Add rollback + version swapper storing previous artifacts in `internal/plugins/rollback.go`.
- [X] T023 [US3] Ensure plugin supervisor auto-starts active plugins at boot and on install in `internal/plugins/supervisor.go`.
- [X] T024 [P] [US3] Implement telemetry client + batching in `internal/plugins/telemetry_client.go` with retry logic:
  - T024a: Update status telemetry (plugin version, enabled/disabled state changes) honoring `settings.marketplace.telemetry_opt_in` at enqueue-time.
  - T024b: General marketplace interaction telemetry (catalog browse counts, install/update events per FR-013) with same opt-in enforcement.
- [X] T025 [US3] Build installed-plugin management UI (updated `web/ui/pages/plugins.html`) with update/disable/rollback actions.
- [ ] T026 [P] [US3] Add integration tests for update/rollback + telemetry acknowledgements in `internal/plugins/manager_test.go`.

---

## Phase 6: User Story 4 – Offline Continuity & Manual Imports (Priority: P2)
**Goal**: Allow manual USB/file installs when offline, keep cached artifacts usable, and process revocation feeds once online.

**Independent Test**: Disconnect WAN, install plugin from local `.utplugin`, verify validation + install succeed, then reconnect and confirm revocation sync disables a revoked plugin with alert.

### Implementation & Tests
- [X] T027 [P] [US4] Implement manual import parser (zip reader + manifest validation) in `internal/plugins/importer.go`.
- [X] T028 [US4] Add "Install from file" endpoint + file upload handling in `internal/pages/plugin_api.go` with disk budget checks.
- [X] T029 [US4] Build USB/file picker UI partials in `web/ui/partials/plugin_manual_import.html` showing validation feedback.
- [X] T030 [US4] Implement revocation sync loop + alerts in `internal/server/scheduler.go` and surface banners in `internal/pages/plugins_page.go`.
- [ ] T031 [P] [US4] Create importer + revocation tests in `internal/plugins/importer_test.go` and `internal/server/scheduler_test.go` (revocation path).
- [ ] T031a [Edge Case] Add test for revocation arriving during critical plugin hooks (sale completion) - ensure safe disable after transaction completes.
- [ ] T031b [Edge Case] Add validation + test for dev-override rejection when dev-mode is off (log warning, preserve existing endpoint).
- [ ] T031c [Edge Case] Add validation + test for malformed URLs (non-HTTP schemes, invalid ports) with actionable error messages.
- [ ] T031d [Edge Case] Add handling + test for self-signed certs in dev-mode with explicit warning logs.

---

## Phase 7: User Story 5 – Admin Controls & Permissions (Priority: P3)
**Goal**: Gate installs/updates behind manager overrides, expose audit views, and ensure plugin actions respect RBAC + logging.

**Independent Test**: Attempt install as cashier (prompt for manager PIN), deny override to confirm block, then approve and review audit log filtered by "Marketplace" showing actor + manifest hash.

### Implementation & Tests
- [ ] T032 [P] [US5] Extend settings & policy loader in `internal/settings/settings.go` / `internal/settings/runtime.go` for marketplace RBAC flags and expose the `telemetry_opt_in` toggle used by US3 telemetry client.
- [ ] T033 [US5] Update UI flows (`web/ui/pages/plugins_store.html`) to require manager PIN for restricted actions and show permission badges.
- [ ] T034 [US5] Implement audit log filter + new route in `internal/pages/plugins_page.go` to view marketplace actions.
- [ ] T035 [P] [US5] Add RBAC enforcement tests in `internal/pages/plugins_page_test.go` and `internal/plugins/authorizer_test.go`.

---

## Phase 8: Polish & Cross-Cutting Concerns
- [ ] T036 [P] Update `docs/data-model.md` and `specs/009-cloud-marketplace/quickstart.md` with any schema/flow changes discovered during implementation.
- [ ] T037 Add smoke script coverage for catalog→install→rollback in `scripts/smoke_quickstart/main.go` and ensure quickstart steps remain accurate.
- [ ] T038 [P] Instrument `/plugins/store` render timings (excluding WAN) in `internal/pages/plugins_page.go` and extend `scripts/smoke_quickstart/main.go` to fail if 90th percentile render time exceeds 3 seconds (SC-001).

---

## Phase 9: User Story 6 – Dev Mode Local Marketplace Override (Priority: P2)
**Goal**: Allow dev-mode-only override of marketplace base URL with validation, toggle, and fallback.

**Independent Test**: Enable dev mode, set a local URL, confirm requests route locally; toggle to cloud and back; invalid URL is rejected with actionable error; timeout triggers fallback to cloud with log.

### Implementation & Tests
- [ ] T039 [P] Implement dev-mode-only override config + validation (scheme/host/port/reachability) in `internal/settings` + `internal/plugins/marketplace/client.go`; keep previous endpoint on failure.
- [ ] T040 [US6] Add runtime toggle and fallback logic (health check + timeout) with audit/logging in marketplace client/router; ensure dev-mode gating.
- [ ] T041 [P] Update UI/API surfaces (`internal/pages/plugins_page.go`, templates) and quickstart docs to expose override/toggle; add tests covering toggle, validation errors, and fallback.
---

## Dependencies & Execution Order

1. **Phase 1 → Phase 2**: Setup must complete before foundational services; no user story work until Phase 2 tasks (T004–T009) are done.
2. **Phase 2 → User Stories**: All user stories depend on OAuth, marketplace client, cache, RBAC, and scheduler stubs.
3. **User Story Order**: US1 (catalog) is the MVP and must finish before other stories rely on catalog data. US2 depends on US1 metadata, while US3–US5 can proceed once US2 exposes install data. US4 (manual/offline) depends on install pipeline; US5 builds atop RBAC + audit from earlier phases. US6 (dev override) depends on foundational client/config work and can run in parallel with US3–US5 once Phase 2 completes.
4. **Polish Phase**: Runs after desired user stories merge; focuses on docs/smoke coverage.

Graph (simplified):
```
Setup → Foundational → US1 → US2 → {US3, US4, US5, US6 in priority order}
                       ↘ after US3/US4/US5/US6 → Polish
```

## Parallel Execution Examples
- **US1**: Work on T010 (catalog repo) and T013 (UI templates) in parallel once T012 handler contract is defined; tests (T014) can run concurrently after repo implementation.
- **US2**: T015 download manager and T016 manifest verifier can proceed simultaneously; UI modal (T018) is parallel-friendly once API contract (T017) is stubbed.
- **US3**: T021 update checker and T024 telemetry client are independent until integration tests (T026) run.
- **US4**: T027 importer and T030 revocation loop can develop in parallel; tests (T031) execute once respective modules stabilize.
- **US5**: T032 settings update and T035 RBAC tests can start while UI changes (T033) are underway.

## Implementation Strategy
1. **MVP (US1)**: Finish Setup + Foundational, then deliver catalog browsing with offline cache (US1). Ship as first milestone.
2. **Incremental Delivery**:
   - Add US2 installs/resume for tangible plugin delivery.
   - Layer US3 lifecycle management + telemetry to stabilize devices.
   - Implement US4 offline/manual import + revocation for resilience.
   - Conclude with US5 admin controls for governance.
3. **Validation**: After each story, run targeted tests (`go test ./internal/plugins/...`) and manual quickstart steps before moving on.
