# Functional Checklist: POS Core MVP

**Purpose**: Validate the POS core MVP across catalog, sales, inventory, and the basic plugins host using the existing SQLite schema (`internal/db/migrations/001_init.sql`).  
**Created**: 2025-11-27  
**Feature**: specs/pos-core-mvp/spec.md

## Constitution & Schema Alignment

- [ ] CHK001 No schema changes; all writes use existing tables from `001_init.sql` with `PRAGMA foreign_keys = ON`.
- [ ] CHK002 All money stored as INTEGER minor units; weighed quantities use REAL; `(item_id XOR variant_id)` checks enforced.
- [ ] CHK003 Price resolution uses latest `price_history` entry per item/variant; updates append history, never overwrite.

## Catalog & Pricing

- [ ] CHK004 Items/variants can be created/updated with barcodes, images, category, brand, tax code; inactive (`is_active=0`) records are excluded from selection.
- [ ] CHK005 Primary/secondary barcodes resolve to the correct item/variant; fallback search (SKU/name) works when barcode missing.
- [ ] CHK006 Price changes create a new `price_history` row with timestamps; previous prices remain queryable.

## Sales Flow (Offline)

- [ ] CHK007 Basket supports SKU/barcode add, quantity edits (including weighed), remove; `sale_lines` capture snapshots (name, sku/barcode, tax_rate_bp, unit_price, totals).
- [ ] CHK008 Tax-inclusive/exclusive mode driven by `settings`; line tax and totals remain integer-safe with clear rounding rules.
- [ ] CHK009 Sale- and line-level discounts persisted in `sale_discounts` with computed amounts reflected in `sales.discount_total`.
- [ ] CHK010 `sales.subtotal`, `tax_total`, `total`, and optional rounding fields persisted on completion; `receipt_no` unique per sale.

## Payments & Completion

- [ ] CHK011 Multiple payments per sale using seeded `payment_methods`; `payments` capture amount, currency, reference, change_given, paid_at.
- [ ] CHK012 Sale only completes when payments cover total; `sales.status`/`completed_at` set; partial/failed tenders roll back cleanly.
- [ ] CHK013 Park/void flows keep `sale_lines` without stock movements; only completed sales post stock changes.

## Inventory & Stock Movements

- [ ] CHK014 Completing a sale posts `stock_movements(type='sale')` per line with negative qty and `sale_line_id` link.
- [ ] CHK015 `inventory` aggregates reflect movements per `stock_location`; weighed items preserve REAL precision.
- [ ] CHK016 Returns/refunds modeled as new sales with `sale_type='return'` and `sale_links`; movements are positive and auditable.
- [ ] CHK017 Negative inventory blocked or requires explicit manager override that is logged.

## Plugins Host (Basic)

- [ ] CHK018 Plugin metadata persists in `plugins`, `plugin_entries`, `plugin_settings`, `plugin_permissions` from manifest data.
- [ ] CHK019 UI shell renders plugin entries (page/button/popup/customer_facing/receipt_template) based on stored records and respects declared permissions/capabilities.

## Audit, Resilience, Safety

- [ ] CHK020 `audit_log` records sale lifecycle, inventory adjustments, and plugin enable/disable with actor + payload.
- [ ] CHK021 Offline flow (scan → cart → payment → receipt) works without network; retries avoid duplicate rows.
- [ ] CHK022 Errors do not leave orphaned `sale_lines`/`payments` without a `sales` row; transactions remain atomic per operation.
