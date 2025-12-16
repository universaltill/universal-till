# Feature Specification: Complete Pending Marketplace & Catalog Tasks

**Feature Branch**: `010-complete-pending-specs`  
**Created**: 2025-12-09  
**Status**: Draft  
**Input**: User description: "Finish outstanding spec tasks (excluding 000-pos-core-mvp, formerly 001-pos-core-mvp): Cloud Marketplace tests/RBAC/dev-override/polish (T020, T026, T031/T031a/T031b/T031c/T031d, T032-T041, T036-T038) plus Catalog & Pricing CA-403 docs update."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Marketplace Resilience Coverage (Priority: P1)

Operators and QA can rely on the marketplace client to handle failed downloads, manifest issues, updates, rollbacks, and revocations without corrupting state or leaving devices misaligned with the cloud.

**Why this priority**: These flows guard device stability and data integrity; gaps here risk bricked plugins or mismatched inventories after failures.

**Independent Test**: Run the marketplace regression suite covering download/manifest failure paths, update+rollback with telemetry acknowledgements, manual import plus revocation (including while critical hooks are running); all scenarios complete with clean rollback/audit outcomes.

**Acceptance Scenarios**:

1. **Given** a plugin install where the download is interrupted or the manifest is invalid, **When** the install flow retries or aborts, **Then** the operator sees a clear failure reason, partial artifacts are cleaned up, and no plugin is registered until validation succeeds.
2. **Given** a device updating or rolling back a plugin with telemetry enabled, **When** the action completes, **Then** previous artifacts remain available for rollback, the active version matches the last confirmed action, and telemetry acknowledgements reflect the final state without duplicates.
3. **Given** a revocation feed arriving during a critical plugin hook (e.g., sale completion) or after a manual import, **When** revocation processing runs, **Then** the plugin disables safely after the critical section finishes, alerts the operator, and audit records capture the enforcement.

---

### User Story 2 - Admin Controls & Audit (Priority: P1)

Managers can gate marketplace actions with overrides, see permission expectations in the UI, and review an audit trail filtered to marketplace events.

**Why this priority**: Governance and accountability are mandatory before expanding marketplace use beyond early adopters.

**Independent Test**: Attempt restricted marketplace actions as a non-manager to confirm PIN prompts and enforcement; approve/deny to verify audit entries and permission badges; open the marketplace audit filter to see scoped events only.

**Acceptance Scenarios**:

1. **Given** a cashier attempting to install, update, or override marketplace endpoints, **When** the action is initiated, **Then** a manager PIN prompt blocks progress until approved and denial leaves no state change.
2. **Given** a manager completes or rejects a marketplace action, **When** the audit log is filtered by “Marketplace”, **Then** the view lists actor, action, target plugin, and outcome with timestamps for that action set.
3. **Given** marketplace listings and actions, **When** the UI renders, **Then** permission badges and override requirements are visible so operators know which actions require manager approval.

---

### User Story 3 - Dev-Mode Override Safety (Priority: P2)

Developers can point the marketplace client at a local endpoint in dev mode with validation, health checks, and fallbacks that prevent accidental misuse in production.

**Why this priority**: Override controls enable safe local iteration while guarding production devices from misconfiguration or insecure endpoints.

**Independent Test**: Toggle dev-mode override to a valid local URL to confirm routing switches and can be reverted; attempt invalid/malformed/unauthorized URLs and observe clear errors with the cloud endpoint preserved; trigger a timeout or self-signed cert and confirm fallback with warnings.

**Acceptance Scenarios**:

1. **Given** dev mode is enabled and a valid override URL is supplied, **When** the operator enables the override, **Then** marketplace traffic routes to the override, health checks pass, and the toggle can be turned off to return to the cloud endpoint.
2. **Given** dev mode is off or an override URL is malformed/unauthorized, **When** an override is attempted, **Then** the system rejects the change with an actionable error, keeps the previous endpoint active, and records the attempt in logs/audit.
3. **Given** an override endpoint that times out or uses a self-signed certificate, **When** the system detects the failure, **Then** it falls back to the last known good endpoint, logs the issue, and informs the operator.

---

### User Story 4 - Documentation, Smoke & Performance Guardrails (Priority: P3)

Teams have up-to-date docs and smoke/performance checks that mirror reality for marketplace flows and catalog/pricing data references.

**Why this priority**: Accurate guides and guardrails reduce rollout risk and keep support burden low.

**Independent Test**: Run the smoke script for catalog → install → rollback and confirm it enforces the render-time threshold; follow quickstart/docs (including catalog/pricing data-model notes) end-to-end without extra steps.

**Acceptance Scenarios**:

1. **Given** the smoke script and `/plugins/store` page, **When** catalog → install → rollback runs, **Then** the flow completes without manual tweaks and fails loudly if render time exceeds the 3-second 90th percentile threshold.
2. **Given** updated quickstart and data-model docs, **When** an operator follows them to configure catalog/pricing and marketplace settings, **Then** the steps match the UI/current flows and no stale schema references remain.

### Edge Cases

- Download retries leave no orphaned partial artifacts and communicate retry vs. final failure clearly.
- Revocation arrives during sale completion; disablement waits until post-transaction and records the action.
- Telemetry opt-in remains respected during updates/rollbacks so no status events send when opt-in is false.
- Dev override attempts with non-HTTP schemes, invalid ports, or when dev mode is off are blocked with explicit guidance.
- Self-signed certificates are accepted only in dev mode with warnings and preserved audit/log messages.

### Dependencies & Assumptions

- Marketplace foundational flows (catalog browse, install, update, rollback, telemetry client, and audit plumbing) from earlier specs remain in place and stable.
- Dev mode toggle already exists and continues to gate override availability; feature work layers validation and fallback on top.
- Smoke script harness exists and can be extended for added marketplace steps and thresholds.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Provide automated coverage for marketplace download and manifest failure paths that surfaces actionable errors, cleans partial artifacts, and blocks plugin registration until validation succeeds.
- **FR-002**: Validate update/rollback flows with telemetry acknowledgements so the active version matches the last confirmed action and acknowledgements are recorded without duplication.
- **FR-003**: Add importer and revocation regression coverage, including revocation during critical plugin hooks, ensuring plugins disable only after safe points and emit clear alerts/audit entries.
- **FR-004**: Enforce dev-mode-only marketplace override validation (scheme/host/port/reachability) and block overrides when dev mode is off or inputs are malformed, preserving the current endpoint on failure.
- **FR-005**: Provide runtime toggle, health check, and fallback behavior for the override, including handling self-signed certificates in dev mode with warnings and automatic fallback on timeouts.
- **FR-006**: Extend marketplace settings and UI to require manager approval for restricted actions, display permission badges, and expose the telemetry opt-in toggle used by lifecycle telemetry.
- **FR-007**: Add audit log filtering for marketplace actions and RBAC enforcement tests that verify both denial and approval paths.
- **FR-008**: Instrument the marketplace store page render time and extend smoke coverage for catalog → install → rollback, failing when the 90th percentile render exceeds 3 seconds.
- **FR-009**: Update marketplace docs and quickstart to reflect new settings/flows, and refresh catalog/pricing data-model notes (CA-403) with any UI path changes.

### Key Entities *(include if feature involves data)*

- **Marketplace Plugin Lifecycle**: Represents plugin artifacts, versions, and state transitions (install, update, rollback, revoke) with corresponding audit and telemetry signals.
- **Marketplace Controls**: Settings that gate overrides, telemetry opt-in, RBAC flags, and manager PIN requirements; includes current/previous endpoint for safe fallback.
- **Audit & Telemetry Record**: Captures actor, action, target plugin, outcome, timestamp, and context (e.g., override attempt, revocation enforcement) for governance and troubleshooting.
- **Operational Playbook**: Documentation and smoke/performance scripts that encode the expected catalog and marketplace flows and thresholds.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Marketplace regression suite covers download/manifest failures, update/rollback with telemetry acknowledgements, importer + revocation (including critical hook timing) and passes 100% of scenarios.
- **SC-002**: All restricted marketplace actions prompt for and enforce manager approval, with 100% of attempts represented in the marketplace audit filter including actor/outcome.
- **SC-003**: Dev-mode override attempts with invalid inputs/dev-mode-off are rejected in 100% of cases without changing the active endpoint; valid overrides route traffic and recover via fallback within the defined timeout window.
- **SC-004**: Smoke run for catalog → install → rollback completes end-to-end and fails if `/plugins/store` render time 90th percentile exceeds 3 seconds; latest quickstart/docs remain in sync with the smoke steps.
- **SC-005**: Catalog/pricing data-model documentation updates apply without discrepancies when replaying UI steps; no stale references are found during doc walkthroughs.
