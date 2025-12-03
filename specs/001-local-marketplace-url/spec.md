# Feature Specification: Local Marketplace URL Override

**Feature Branch**: `001-local-marketplace-url`  
**Created**: 2025-12-03  
**Status**: Draft  
**Input**: User description: "in development mode, I should be able to use local url of market place"

## User Scenarios & Testing *(mandatory)*

<!--
  IMPORTANT: User stories should be PRIORITIZED as user journeys ordered by importance.
  Each user story/journey must be INDEPENDENTLY TESTABLE - meaning if you implement just ONE of them,
  you should still have a viable MVP (Minimum Viable Product) that delivers value.
  
  Assign priorities (P1, P2, P3, etc.) to each story, where P1 is the most critical.
  Think of each story as a standalone slice of functionality that can be:
  - Developed independently
  - Tested independently
  - Deployed independently
  - Demonstrated to users independently
-->

### User Story 1 - Use Local Marketplace Endpoint (Priority: P1)

Developers running Universal Till in dev mode need to point all marketplace requests (catalog browse, install metadata, compatibility checks) to a marketplace instance hosted on their laptop or LAN without modifying production settings.

**Why this priority**: Without this, developers cannot iterate on marketplace APIs offline, so the feature has direct impact on productivity and unblocker for downstream tasks.

**Independent Test**: Enable dev mode, set local base URL, and confirm catalog listings and plugin metadata resolve entirely from the local endpoint.

**Acceptance Scenarios**:

1. **Given** dev mode is enabled and a reachable local marketplace URL is supplied, **When** the POS loads the marketplace catalog, **Then** every request uses the provided local URL instead of the cloud endpoint and completes successfully.
2. **Given** the developer clears the override while dev mode remains on, **When** the POS reloads the marketplace screen, **Then** it falls back to the default cloud endpoint without needing an app restart.

---

### User Story 2 - Toggle Between Local and Cloud (Priority: P2)

QA engineers need to quickly compare behavior between the local stub marketplace and the hosted marketplace to ensure compatibility without editing configuration files manually.

**Why this priority**: Fast toggling keeps test cycles short and reduces misconfigurations when validating compatibility gating or plugin manifests.

**Independent Test**: Use the configuration surface (settings UI or CLI) to switch endpoints and verify requests swap instantly while logging the active source.

**Acceptance Scenarios**:

1. **Given** dev mode is on and both local and cloud endpoints are configured, **When** the engineer selects "cloud" as the active source, **Then** the system routes the next marketplace request to the hosted endpoint and logs the change with timestamp.
2. **Given** the same engineer re-selects "local" later, **When** the health check passes, **Then** the override is re-applied without restarting services.

---

### User Story 3 - Diagnose Local Endpoint Issues (Priority: P3)

Support engineers need immediate, actionable feedback when the configured local URL is invalid, unreachable, or responds with an unexpected schema so they can fix their environment without digging into logs.

**Why this priority**: Reduces support load and wasted time by surfacing misconfiguration causes directly in the dev UI.

**Independent Test**: Configure an invalid URL and confirm that validation errors block activation, document the failure reason, and recommend corrective actions.

**Acceptance Scenarios**:

1. **Given** the override points to a URL that fails TLS or DNS resolution, **When** the developer attempts to save the configuration, **Then** the UI rejects it with the exact error and keeps the previous working endpoint active.
2. **Given** the endpoint is reachable but returns malformed metadata, **When** the catalog load runs, **Then** the system surfaces a structured error (include last URL and HTTP status) instead of a generic failure message.

### Edge Cases

- Local override is provided while dev mode is off (must be ignored with warning).
- Developer enters non-HTTP(S) schemes or malformed URLs.
- Local server requires self-signed certificates or uses HTTP-only endpoints.
- Override was working but the local process stops mid-session (need retry/fallback behavior).
- Multiple developers share the same workstation and need independent overrides.

## Requirements *(mandatory)*

<!--
  ACTION REQUIRED: The content in this section represents placeholders.
  Fill them out with the right functional requirements.
-->

### Functional Requirements

- **FR-001**: The application MUST expose a dev-mode-only configuration surface (settings UI plus optional override flag) that accepts a full base URL for the marketplace service (e.g., `http://localhost:8082`).
- **FR-002**: When dev mode is enabled and a valid local URL is set, ALL marketplace service calls MUST target that URL until the override is cleared or dev mode is disabled.
- **FR-003**: When dev mode is disabled, any stored override MUST be ignored and the system MUST use the default cloud marketplace endpoint without requiring manual cleanup.
- **FR-004**: The system MUST validate the override before activation (scheme, host, optional port) and provide immediate human-readable errors if the URL is malformed or unreachable.
- **FR-005**: Developers MUST be able to switch between local and cloud endpoints at runtime, and the system MUST log the active source plus timestamp for traceability.
- **FR-006**: If the local endpoint stops responding for more than a configurable timeout, the POS MUST surface a non-blocking warning and fall back to the cloud endpoint while keeping devs informed of the failover.

No clarifications requested; assumptions are documented below.

### Key Entities *(include if feature involves data)*

- **MarketplaceEndpoint**: Captures the active base URL, source (`cloud|local`), verification timestamp, last error, and who last changed it (system vs manual). Used by routing logic when issuing marketplace calls.
- **DevModeConfig**: Represents dev-only settings such as `dev_mode_enabled`, `marketplace_override_url`, validation status, and fallback preferences. Persisted locally alongside other development settings.

## Success Criteria *(mandatory)*

<!--
  ACTION REQUIRED: Define measurable success criteria.
  These must be technology-agnostic and measurable.
-->

### Measurable Outcomes

- **SC-001**: A developer can switch the marketplace base URL and observe requests hitting the new endpoint within 60 seconds (including validation and confirmation).
- **SC-002**: 100% of marketplace requests issued while dev mode + override are active must hit the configured local URL during automated tests.
- **SC-003**: At least 90% of misconfiguration attempts (bad scheme, unreachable host) produce actionable error messages without checking raw logs.
- **SC-004**: Fallback to the cloud endpoint occurs within 5 seconds of detecting a dead local endpoint, and the UI notifies the user in the same session.

## Assumptions & Dependencies

- Dev mode gating already exists (`UT_DEV_MODE=true`) and is the only context where overrides should be honored.
- Developers have control over their network/firewall to expose the local marketplace service (no additional tunneling work in scope).
- Marketplace APIs share the same schema locally and in cloud; this work only swaps the base URL, not payload formats.
- Telemetry/analytics about endpoint usage reuse existing logging infrastructure; no new telemetry channels are required.
