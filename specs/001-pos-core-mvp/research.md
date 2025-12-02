# Research: Hybrid Local + Cloud Plugin Model

Date: 2025-12-02

Summary
- Decision: Keep core strictly local-first and open-source. Offer optional cloud-enhanced plugin features that merchants opt into. Plugins run locally by default; cloud mode is an enhancement for premium features (reconciliation, fraud, analytics, multi-location sync).

Rationale
- Local-first preserves the project's open-source credibility, supports offline markets, and enables hardware partnerships.
- Cloud features provide incremental monetization and differentiated value (fraud scoring, multi-channel inventory, backups). Make them clearly optional and additive.

Alternatives considered
- Force cloud (rejected): alienates early adopters and emerging markets.
- Cloud-only (rejected): breaks the open-source promise and offline operability.

Technical considerations
- Plugin runtime: run plugins as separate OS processes for isolation; host communicates via IPC (unix sockets/pipes) or gRPC over loopback. This enables language-agnostic plugins.
- Manifest verification: verify SHA256 and manifest schema at install time; persist manifest metadata in `plugin_catalog` and runtime install rows in `plugins`.
- Sensitive credentials: prefer not to store raw cloud API keys in local DB. For local plugins requiring merchant-owned 3rd-party keys (e.g., Stripe), store keys in `plugin_settings` only if encrypted by the host or rely on OS-level secrets (recommendation).
- Cloud tokens: if a merchant enables cloud features, store cloud-scoped tokens in plugin_settings with encryption and mark trust_level accordingly; provide an admin UI to review trust for cloud-enabled plugins.
- Offline-first behavior: cloud operations must be non-blocking for core flows. Any cloud-enhanced step should be asynchronous with retry and idempotency guarantees.

Operational concerns
- Backups & restore: cloud subscribers get automated backups and cross-device sync; non-cloud users must perform manual backups (`data/unitill.db`).
- Privacy & compliance: clearly document what data is sent to cloud; allow merchants to opt out of each cloud feature.

Open questions (NEEDS CLARIFICATION)
- Encryption strategy for storing API keys on-device (host-provided encryption vs OS secret store) — research and decide.
- Cloud API surface: minimal event set to upload (e.g., `sale.completed`, `inventory.delta`) and retrieval endpoints for sync.

Decisions to be captured in contracts/
- Plugin manifest schema with `modes.local` and `modes.cloud` sections.
- Host ↔ plugin IPC/gRPC handshake: versioned protocol and capability negotiation.
# Research Notes: POS Core MVP

**Date**: 2025-11-27  
**Scope**: Catalog, sales (offline), inventory, basic plugins host using existing SQLite schema (`001_init.sql`).

## Known Inputs

- Schema: `internal/db/migrations/001_init.sql`, documented in `docs/data-model.md`; money = INTEGER minor units; weighed quantities = REAL; `(item_id XOR variant_id)` checks.
- Runtime: Go 1.25, SQLite via `modernc.org/sqlite`, responsive web UI (htmx + Alpine.js).
- Constitution: no schema changes; offline-first; append-only history; plugin contracts enforced; testable core.

## Current Understanding (to verify in code)

- Catalog CRUD paths and where they live (`internal/pos`, `internal/pages`, `web/` templates).
- Price resolution: latest `price_history` row per item/variant; inclusive/exclusive tax behavior via `settings`.
- Sales flow: basket handling, discounts, multi-payment handling, `receipt_no` generation, rounding behavior.
- Inventory: movement posting on sale/return; `inventory` aggregation strategy; negative stock policy.
- Plugins host: manifest ingestion, persistence in `plugins` tables, UI rendering hooks.

## Open Questions / Clarifications Needed

- Exact rounding rules for tax-inclusive vs tax-exclusive pricing; how rounding is stored per line vs sale.
- Policy for negative inventory: block vs allow with override logging.
- How receipt numbering is generated and uniqueness enforced.
- Source of truth for default stock_location during sales when multiple locations exist.
- Plugin permission enforcement path in UI/actions; capability mapping to stored permissions.

## Next Research Actions

- Trace pricing/tax calculation code paths in `internal/pos` and associated helpers.
- Locate inventory aggregation logic and stock movement creation on sale completion.
- Review plugin host routes/handlers in `internal/plugins` and UI in `web/` to map to tables.
- Confirm transaction boundaries in sales/payment flows to prevent orphaned rows.
