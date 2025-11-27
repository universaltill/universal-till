# Functional Checklist: POS Core MVP (Sale Flow)

**Purpose**: Validate the basic in-store sale flow (scan item → cart → payment → receipt) using the existing SQLite schema (`internal/db/migrations/001_init.sql`).  
**Created**: 2025-11-27  
**Feature**: specs/pos-core-mvp/spec.md

## Schema & Data Alignment

- [ ] CHK001 No schema changes; all writes use existing tables from `001_init.sql` (`sales`, `sale_lines`, `payments`, `stock_movements`, `inventory`, catalog tables).
- [ ] CHK002 Money stored as INTEGER minor units; weighed items use REAL quantities; foreign keys and `(item_id XOR variant_id)` constraints are respected.
- [ ] CHK003 Current prices resolved from `price_history` (latest open-ended row); no overwriting of history.

## Scan & Basket

- [ ] CHK004 Barcode lookup hits `item_barcodes` or `variant_barcodes`, prefers primary barcode, and falls back to text/SKU search when not found.
- [ ] CHK005 Adding to basket writes `sale_lines` with snapshots (name, sku/barcode, tax_rate_bp, unit_price, discounts, totals) and links to item or variant exclusively.
- [ ] CHK006 Tax rate comes from the linked `tax_codes` row and is stored on the line; rounding stays integer-safe.
- [ ] CHK007 Basket supports quantity edits (including weighed items) and removes lines without leaving orphaned rows.

## Discounts & Totals

- [ ] CHK008 Sale- or line-level discounts persisted in `sale_discounts` as fixed or percent with computed `amount`; reflected in `sales.discount_total`.
- [ ] CHK009 `sales.subtotal`, `tax_total`, `total`, and optional `rounding` fields are computed and stored at completion.

## Payments & Completion

- [ ] CHK010 Multiple payments per sale are supported using seeded `payment_methods` (cash/card/gift); `payments` capture amount, currency, reference, change_given, paid_at.
- [ ] CHK011 `receipt_no` is unique; `sales.status` transitions to `completed` with `completed_at` set when payments cover total.
- [ ] CHK012 Void/park flows: open/parked sales can be voided without stock movement; only completed sales post stock changes.

## Stock & Inventory

- [ ] CHK013 Completing a sale creates `stock_movements(type='sale')` per line with negative quantity and links `sale_line_id`.
- [ ] CHK014 `inventory` quantities update per `stock_location` accordingly; weighed items maintain REAL precision.
- [ ] CHK015 Returns/refunds are modeled as new sales with `sale_type='return'` linked via `sale_links`, posting positive stock movement.

## Receipt & Audit

- [ ] CHK016 Receipt payload includes line snapshots, tax, discounts, payments, and totals; printable/exportable without additional schema.
- [ ] CHK017 `audit_log` records sale lifecycle changes (create, complete, void, refund) with actor and payload JSON.

## Offline & Safety

- [ ] CHK018 Full scan→cart→payment→receipt flow works offline; no external services required.
- [ ] CHK019 Prevent negative inventory unless manager override is explicitly allowed and recorded.
- [ ] CHK020 Error handling preserves data integrity (no partial `sale_lines`/`payments` without matching `sales` row).
