# Plan: Catalog & Pricing Core (002a)

Branch: `002-pos-core-mvp` | Inputs: specs/002a-catalog-pricing/spec.md, existing schema `internal/db/migrations/001_init.sql`

## Summary
Implement catalog CRUD, barcode enforcement, active/inactive filtering, and append-only price history using existing Go code paths and SQLite schema. No migrations allowed.

## Technical Notes
- Go 1.25; SQLite via modernc.org/sqlite.
- Touch areas: `internal/pos`, `internal/pages`, `web/` templates, `internal/db` helpers.
- Reuse price helper patterns and integer money conventions.

## Phases
1) Baseline & Helpers
- Review existing catalog models/handlers; confirm FK/constraint expectations.
- Add helpers for `(item_id XOR variant_id)` barcode validation and price resolution.

2) CRUD & UI Wiring
- Implement create/update/deactivate for items/variants with barcodes/images/category/brand/tax code.
- Wire HTMX/handlers for forms; ensure inactive hidden by default searches.

3) Price History
- Implement append-only writes on price change; guard against in-place edits.
- Add read helpers returning latest price per item/variant.

4) Tests & Docs
- Unit tests for barcode validation, price resolution, active filtering.
- Handler tests for CRUD paths; update quickstart if flows change.

## Constraints
- No schema changes; integer minor units only.
- Offline-first; avoid network dependencies.
- Must keep domain logic testable without UI/network.

## Deliverables
- Updated handlers/templates for catalog CRUD.
- Helpers enforcing barcode rules and price history append-only writes.
- Tests covering CRUD, price resolution, active filtering.
- Doc updates as needed (data-model/quickstart notes).
