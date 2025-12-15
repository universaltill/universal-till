# Research: SQL Access Refactor to Data Repos

**Purpose**: Resolve unknowns and capture decisions before design.  
**Feature**: specs/001-sql-repo-refactor/spec.md  
**Date**: 2025-12-09

## Findings

- **Decision**: Keep `database/sql` repository methods as the sole owners of SQL strings; business layers call interfaces only.  
  **Rationale**: Centralizes queries, simplifies audits, and enables consistent telemetry/error handling.  
  **Alternatives**: Leave inline SQL in handlers/plugins — rejected because it fragments logic and raises regression risk.

- **Decision**: Support caller-managed transactions by accepting `*sql.Tx`/`TxWrapper` in repository methods where flows already use transactions.  
  **Rationale**: Preserves commit/rollback semantics and avoids double-opening connections.  
  **Alternatives**: Auto-open transactions per method — rejected; would change transactional scope and performance.

- **Decision**: Standardize error wrapping with sentinel/classification where it already exists; do not alter user-facing messages.  
  **Rationale**: Maintains existing behavior while enabling consistent logging/metrics at the repo boundary.  
  **Alternatives**: Introduce new error taxonomy now — deferred to avoid scope creep.

- **Decision**: Reuse/centralize shared queries to avoid duplication (helpers inside repo files or shared query builders).  
  **Rationale**: Prevents drift and inconsistent parameterization.  
  **Alternatives**: Duplicate per caller — rejected.

## Open Items

- None; proceed to design.
