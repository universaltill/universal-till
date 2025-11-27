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
