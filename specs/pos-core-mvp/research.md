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
