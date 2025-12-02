# Data Model: Hybrid Plugin Metadata (pos-core-mvp)

Date: 2025-12-02

Purpose
- Describe how cloud-enhanced plugin metadata and local-only plugin data map to existing schema without adding migrations for MVP.

Key existing tables used
- `plugin_catalog` — store canonical manifests (id, version, package_url, sha256, runtime, entrypoint, author)
- `plugins` — installed plugin rows (id, name, version, is_active, trust_level)
- `plugin_entries` — UI and entry registrations (page/button/popup)
- `plugin_settings` — per-plugin key/value configuration (use for local-mode credentials or cloud tokens; prefer encryption)
- `plugin_permissions` / `plugin_hooks` — capabilities and event subscriptions

Design notes (no schema changes)
- Local mode
  - Manifest installed into `plugin_catalog` and `plugins` with `runtime`/`entrypoint` for local process invocation.
  - Merchant-provided 3rd-party API keys (e.g., Stripe) may be stored in `plugin_settings` with host-side encryption; alternatively, encourage OS secret stores and only reference them from `plugin_settings` as a key identifier.

- Cloud mode
  - Cloud-specific tokens or flags live in `plugin_settings` under a `cloud.*` namespace (e.g., `cloud.enabled=true`, `cloud.token_id=<id>`). The actual token material should be encrypted or stored in the cloud vault; `plugin_settings` holds a pointer/metadata only when possible.
  - Cloud sync metadata (last_sync_at, sync_cursor) can be stored in `plugin_settings` for each plugin. Keep values small to avoid DB bloat.

Sync & Reconciliation
- Do not require new tables for initial MVP. Use background worker processes to push `sale.completed` events to cloud endpoints. For reconciliation, cloud returns match ids that the local host records via `plugin_settings` or `plugin_hooks` audit entries.

Security
- Sensitive values should be encrypted at rest. Recommend host provides an encryption key managed via environment variables or OS key store. Document migration path later for secure key rotation.

Limitations
- This design intentionally avoids schema migrations for MVP. If later we need scalable sync (change tracking, conflict resolution), add dedicated sync tables and robust conflict policies in a follow-up migration.
# Data Model Notes: POS Core MVP

**Date**: 2025-11-27  
**Scope**: Use existing schema (`internal/db/migrations/001_init.sql`) without modifications. This file maps feature behaviors to current tables.

## Money & Quantities

- Monetary fields stored as INTEGER minor units (e.g., pence).  
- Weighed quantities use REAL; non-weighed use INTEGER.  
- Foreign keys ON; shared check: `(item_id IS NOT NULL AND variant_id IS NULL) OR (item_id IS NULL AND variant_id IS NOT NULL)`.

## Key Tables Used

- Catalog: `items`, `item_variants`, `item_barcodes`, `variant_barcodes`, `item_images`, `categories`, `brands`, `tax_codes`.  
- Pricing: `price_history` (append-only).  
- Sales: `sales`, `sale_lines`, `sale_discounts`, `payments`, `sale_links`, `payment_methods`.  
- Inventory: `stock_movements` (source of truth), `inventory` (aggregated).  
- Plugins: `plugin_catalog`, `plugins`, `plugin_entries`, `plugin_settings`, `plugin_permissions`, `plugin_hooks`.  
- Audit/ops: `audit_log`, `settings`, `registers`, `users`, `customers`, `stock_locations`.

## Behaviors Bound to Schema (no changes)

- Price resolution: latest `price_history` row per item/variant (open-ended effective range); no overwrites.  
- Sales persist snapshots on `sale_lines` (name, sku/barcode, tax_rate_bp, unit_price, totals).  
- Discounts captured in `sale_discounts` (line or sale level, fixed/percent) with computed `amount`.  
- Payments: multiple per sale via seeded `payment_methods`; amounts in integer minor units; `change_given` stored.  
- Inventory: `stock_movements` appended for sales/returns/adjustments; `inventory` reflects aggregated on-hand per location.  
- Returns: modeled as new sales with `sale_links` and positive stock movements.  
- Plugin host: metadata stored across plugin tables; permissions/capabilities enforced at load time.

## Data Integrity Requirements

- FK violations rejected; ensure operations run with `PRAGMA foreign_keys = ON`.  
- Inactive (`is_active=0`) catalog entities excluded from sale selection.  
- Rounding rules for tax-inclusive/exclusive pricing must remain integer-safe.  
- No migration files added/edited for this feature.

## Rounding Test Vectors (half-up)

Include these explicit test vectors in unit tests (T505) to validate deterministic rounding semantics at the line and tax level.

1) Unweighted (integer qty)
  - unit_price = 100 (minor units, e.g., £1.00), qty = 1.000 → raw = 100.0 → rounded = 100
  - unit_price = 100, qty = 1.004 → raw = 100.4 → rounded = 100
  - unit_price = 100, qty = 1.005 → raw = 100.5 → rounded = 101

2) Weighted (REAL qty)
  - unit_price = 1000 (minor units/kg, £10.00/kg), qty = 0.333 → raw = 333.0 → rounded = 333
  - unit_price = 1000, qty = 0.3333 → raw = 333.3 → rounded = 333
  - unit_price = 1000, qty = 0.3335 → raw = 333.5 → rounded = 334

3) Tax rounding (line-level)
  - line_pre_tax = 100, tax_rate = 20% (2000 bp) → tax_raw = 20.0 → rounded_tax = 20
  - line_pre_tax = 101, tax_rate = 20% → tax_raw = 20.2 → rounded_tax = 20

Use these vectors in table-driven unit tests that assert both line rounding and tax rounding, and verify `sales.rounding` records any residual signed difference between summed rounded line values and expected arithmetic totals.
