# Feature Specification: SQL Access Refactor to Data Repos

**Feature Branch**: `001-sql-repo-refactor`  
**Created**: 2025-12-09  
**Status**: Draft  
**Input**: User description: "Refactor all functions that run SQL queries/commands into internal/data/*_repo.go layers, consolidating data access behind repositories and reducing inline SQL in business logic."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Centralize Data Access (Priority: P1)

Developers can add or change data logic by working in repository files rather than hunting inline SQL across handlers, services, or plugins.

**Why this priority**: Centralization lowers regressions and speeds delivery; scattered SQL is costly to maintain and hard to audit.

**Independent Test**: Run a static scan to confirm business layers contain no direct SQL while repository files contain the expected queries; unit tests for moved functions still pass.

**Acceptance Scenarios**:

1. **Given** existing features that previously called inline SQL, **When** the codebase is scanned, **Then** all SQL queries/commands reside in repository files under `internal/data/` and business code calls repository methods instead.
2. **Given** a developer updating a query, **When** they open the repository file, **Then** the query is discoverable with accompanying tests and does not require edits in unrelated service/handler files.

---

### User Story 2 - Safeguard Transactions & Errors (Priority: P1)

Engineers can rely on repository methods to preserve transactional boundaries and consistent error handling when SQL moves out of business code.

**Why this priority**: Refactors must not change transactional behavior or silently alter error semantics.

**Independent Test**: Execute representative flows (e.g., write + related reads) under transaction and failure conditions to verify commit/rollback behavior and error mapping remain unchanged after the move.

**Acceptance Scenarios**:

1. **Given** a multi-step flow requiring a transaction, **When** repository methods are invoked inside a transaction, **Then** commits and rollbacks behave as before and partial writes are not persisted on failure.
2. **Given** a query that previously returned specific error semantics, **When** the repository method surfaces errors, **Then** callers receive equivalent classifications/messages to support existing handling paths.

---

### User Story 3 - Observability & Tests Follow Data (Priority: P2)

Repository methods expose consistent logging/metrics hooks and have direct tests, so diagnosing data issues no longer requires digging through handlers.

**Why this priority**: Data-layer observability and tests must stay attached to the queries after relocation.

**Independent Test**: Run repository-focused tests that assert logging/metrics hooks are invoked on success/failure and validate query outputs; verify handlers remain free of SQL but still emit expected telemetry via repository calls.

**Acceptance Scenarios**:

1. **Given** repository methods for reads/writes, **When** they execute successfully, **Then** they emit the expected telemetry/logs and tests verify the outputs.
2. **Given** a repository method that encounters an error, **When** it returns to callers, **Then** it emits error telemetry/logs and callers do not add duplicate SQL or logging.

---

### Edge Cases

- Multi-statement operations (e.g., insert + audit) must remain atomic when relocated.
- Long-running or paginated queries keep existing limits and ordering to avoid performance regressions.
- Raw SQL previously embedded in tests or mocks is consolidated or wrapped without breaking fixtures.
- Error messages that surface to users retain clarity after repository error mapping.
- New repository methods avoid duplicating query strings across files to prevent drift.

### Dependencies & Assumptions

- Current data models and connection management remain unchanged; the refactor only moves SQL behind repositories.
- Transaction boundaries are already supported by the data layer and can wrap repository calls.
- Existing tests for features using SQL are available to detect regressions after relocation.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Inventory and relocate all SQL queries/commands from business/service/handler/plugin layers into repository files under `internal/data/*_repo.go`, leaving callers with repository method invocations only.
- **FR-002**: Preserve transactional behavior for multi-step operations by ensuring repository methods support execution within caller-managed transactions without altering commit/rollback semantics.
- **FR-003**: Maintain existing error semantics by mapping repository errors to the same categories/messages previously surfaced to callers and users.
- **FR-004**: Provide or update repository-level unit/integration tests that cover each moved query (success and failure paths) and verify outputs match pre-refactor expectations.
- **FR-005**: Ensure logging/metrics hooks for data operations are invoked from repository methods so handlers/services no longer implement SQL-specific telemetry.
- **FR-006**: Eliminate duplicate query definitions by centralizing shared queries in repository helpers and removing redundant SQL strings from the codebase.
- **FR-007**: Keep performance characteristics stable (pagination limits, query shapes, indexes used) when relocating SQL, with regression checks on representative flows.
- **FR-008**: Update documentation or developer notes to point contributors to repository files for data access patterns and to discourage inline SQL in new code.

### Key Entities *(include if feature involves data)*

- **Data Repositories**: Abstractions encapsulating all SQL for each domain area, exposing methods for reads/writes and accepting transaction/context inputs from callers.
- **Business Layers (Handlers/Services/Plugins)**: Consumers of repository methods that orchestrate flows without embedding SQL or database-specific logic.
- **Telemetry & Testing Artifacts**: Logs/metrics tied to repository operations and test suites validating repository behavior (success, failure, and transactional cases).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of SQL queries/commands are removed from business-layer files and present only in `internal/data/*_repo` files, confirmed via repository scan.
- **SC-002**: All repository methods touched have automated tests covering success and failure paths; existing feature tests continue to pass after refactor.
- **SC-003**: Transactional flows observed pre-refactor exhibit identical commit/rollback outcomes post-refactor in regression tests.
- **SC-004**: No statistically significant performance regression (>5% runtime increase on representative queries/flows) after relocation, measured on existing benchmarks or targeted checks.
- **SC-005**: Developer docs/guides reference repository locations for data access, and code review checks show no new inline SQL added during the effort.
