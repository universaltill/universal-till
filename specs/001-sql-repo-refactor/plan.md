# Implementation Plan: SQL Access Refactor to Data Repos

**Branch**: `001-sql-repo-refactor` | **Date**: 2025-12-09 | **Spec**: specs/001-sql-repo-refactor/spec.md  
**Input**: Feature specification from `/specs/001-sql-repo-refactor/spec.md`

## Summary

Move all SQL queries/commands out of handlers/services/plugins into repository files under `internal/data/*_repo.go`, keeping business layers free of SQL. Preserve transactions, error semantics, observability, and performance; add/extend repository-level tests and documentation to direct future data access through repos.

## Technical Context

**Language/Version**: Go 1.25, SQLite via database/sql  
**Primary Dependencies**: Stdlib + SQLite driver (existing), internal data access helpers  
**Storage**: SQLite (local-first)  
**Testing**: `go test ./...` with repository-focused unit/integration coverage  
**Target Platform**: POS backend services (Linux/macOS dev); UI/handlers consume repos  
**Project Type**: Backend (Go monorepo with POS + plugins)  
**Performance Goals**: No regression (>5% slower) on representative queries/flows  
**Constraints**: Maintain existing schema and domain semantics; no migrations expected  
**Scale/Scope**: Moderate codebase; focus on consolidating all SQL into `internal/data`

- Changes MUST respect the existing SQLite schema defined in `internal/db/migrations/001_init.sql` and documented in `docs/data-model.md`.
- Do not rename or drop columns/tables unless the spec explicitly calls for a migration and data migration strategy.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*  
- No schema changes without migrations → respected (refactor only).  
- Keep domain logic testable and free of DB coupling → repository interfaces only in business layers.  
- Sensitive data not logged → preserve/centralize error handling/logging in repos.  
- Backend-led changes before UI → ensure handlers switch to repos first.  
Gate status: PASS (no violations identified).

## Project Structure

### Documentation (this feature)

```text
specs/001-sql-repo-refactor/
├── plan.md              # This file (/speckit.plan output)
├── research.md          # Phase 0 output (/speckit.plan)
├── data-model.md        # Phase 1 output (/speckit.plan)
├── quickstart.md        # Phase 1 output (/speckit.plan)
├── contracts/           # Phase 1 output (/speckit.plan)
└── tasks.md             # Phase 2 output (/speckit.tasks)
```

### Source Code (repository root)

```text
internal/
├── data/                # target repositories for all SQL
├── db/migrations/       # existing schema (no change expected)
├── pages/               # handlers consuming repos
├── plugins/             # plugin host consuming repos
├── pos/                 # POS domain/services to decouple from SQL
└── ... (other domain packages)

core/                    # domain logic (DB-agnostic)
docs/                    # data model references
scripts/                 # tooling/tests/benchmarks
specs/001-sql-repo-refactor/  # plan/spec/research artifacts
web/                     # UI templates invoking handlers
go.mod / go.sum          # Go module
```

**Structure Decision**: Consolidate SQL into `internal/data/*_repo.go`; business logic in `internal/pages`, `internal/plugins`, `internal/pos`, etc., must depend on repository interfaces only. Tests live alongside packages (`*_test.go`).

## Complexity Tracking

No constitution violations or extra projects; tracking not required.

## Phase 0: Research & Unknowns

- Unknowns/clarifications: None identified; refactor-only.  
- Research tasks: Best practices for repository boundaries in Go with `database/sql`, transaction propagation, error wrapping, and avoiding duplicate query strings.  
- Output: `specs/001-sql-repo-refactor/research.md`.

## Phase 1: Design & Contracts

- Data model: No new entities; document repository mapping to existing tables and transaction patterns in `data-model.md`.  
- Contracts: No external API changes; note “no new contracts” in `contracts/README.md`.  
- Quickstart: Add contributor notes on locating SQL, adding repo methods, and testing in `quickstart.md`.  
- Agent context: Run `.specify/scripts/bash/update-agent-context.sh codex` after writing design docs.  
- Outputs: `data-model.md`, `contracts/`, `quickstart.md`, updated agent context marker.

## Phase 2: Prep for Tasks

- Use `/speckit.tasks` after this plan to break refactor into testable work items (by domain: catalog/inventory/sales/plugins/settings/UI).  
- Ensure mapping from removed inline SQL to new repo methods is captured in tasks with test expectations.
