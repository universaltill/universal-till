# POS Core MVP – Catalog, Sales, Inventory, Plugin Host

Author: Core team  
Status: Draft (MVP scope)  
Schema sources: `internal/db/migrations/001_init.sql`, `docs/data-model.md`  
Principles: local-first, offline-capable, integer money, append-only migrations

## Purpose & Goals
- Deliver a usable in-store POS that can run offline on modest hardware.
- Cover the three core domains: Catalog (items/variants/pricing), Inventory (stock per location), Sales (basket → payment → receipt), plus a minimal plugin host that matches existing schema contracts.
- Stay within the relational model already defined; no schema changes are allowed for this MVP.

## Non-Goals
- No cloud sync, multi-store orchestration, or remote management.
- No advanced pricing/promotions beyond fixed/percent discounts captured on sale/sale_line.
- No loyalty, gift-card balance engine, or customer accounts beyond basic contact fields.
- No plugin marketplace; only local install/enable/disable and manifest registration.
- No tax plugin overrides; use stored tax_code rates only.

## Roles & Devices
- `cashier` (primary POS operator) – can sell, take payments, park/void sales, open/close shift.
- `manager` – above plus catalog edits, stock adjustments, returns approval, plugin enable/disable.
- `admin` – superset; can change settings and trust plugins.
- Registers map to stock locations; shifts are opened per register + cashier.

## Data Model Alignment (must use these tables as-is)
- Catalog: `items`, `item_variants`, `item_barcodes`, `variant_barcodes`, `item_images`, `categories`, `brands`, `tax_codes`, `price_history`.
- Inventory: `stock_locations`, `inventory`, `stock_movements`.
- Sales: `sales`, `sale_lines`, `sale_discounts`, `payments`, `payment_methods`, `sale_links` (returns), `customers`, `registers`, `users`, `shifts`, `audit_log`.
- Plugins & UI extras: `plugin_catalog`, `plugins`, `plugin_entries`, `plugin_settings`, `plugin_hooks`, `plugin_permissions`, `shortcut_buttons`.
- Money stored as INTEGER minor units; weighted items use REAL quantities.

## Functional Scope

### Catalog
- CRUD items/variants with category, brand, tax_code, unit, base_price, cost_price, is_weighed flag.
- Multiple barcodes per item or variant; mark one as primary.
- Attach image paths (stored only as paths).
- Price history entries recorded on create/update; current price = latest row without `ends_at`.
- Basic search/browse by text, category, barcode/PLU; show active only by default.

### Inventory
- Stock per `stock_location`; default seed locations from migration are valid.
- Manual adjustments create `stock_movements(type='adjust')` and update `inventory.quantity`.
- Automatic stock movements on sale/return lines (negative for sales, positive for returns).
- Reorder level surfaced for low-stock alerts (no automation beyond flagging).

### Sales & Payments
- Sale lifecycle: open (optional parked), add lines, apply discounts, take payments, complete; support void and refund (refund modeled as new sale linked via `sale_links`).
- Sale line captures snapshots: name, sku/barcode, tax rate bp, unit_price, discount, totals.
- Discounts: fixed or percent at sale or line level → stored in `sale_discounts`.
- Payments: multiple per sale; use seeded `payment_methods` (cash, card, gift); capture reference/change.
- Rounding field available but default 0 (GBP integer).
- Register and cashier recorded; optional customer link.
- Receipt numbering uses unique numeric `receipt_no`; receipts render printable view with barcode, print button, new-customer reset, and auto-reset after inactivity.

### Shifts & Cash Drawer
- Open/close shift per register + cashier; record opening_cash, closing_cash, expected_cash, note.
- Cash-based expected calculation derived from cash payments during shift minus payouts/adjustments (manual entry for now).

### Audit
- Record create/update/void/refund/install/uninstall in `audit_log` with actor_id, entity_type/id, action, payload JSON.

### Plugin Host (minimal)
- Load plugin metadata into `plugin_catalog`; install plugins into `plugins` with runtime + entrypoint.
- Enforce declared permissions/capabilities via `plugin_permissions`; default trust_level = `untrusted`.
- Register UI/actions via `plugin_entries` (page/button/popup/payment/device/etc) and per-plugin config via `plugin_settings`.
- Subscribe to events via `plugin_hooks` (e.g., `sale.completed`); host dispatches events synchronously/offline.
- Enable/disable plugins; uninstall removes plugin rows and cascading entries/settings/hooks/perms.

### Shortcuts
- Map barcode/PLU buttons to items via `shortcut_buttons` for quick-add on POS UI.

## Workflows (Happy Path)
- **Create item/variant** → insert row(s), barcodes/images, initial price_history; optional inventory seed per location.
- **Sell item** → find by barcode/search → add `sale_lines` with snapshot + tax → compute totals → collect `payments` → set `sales.status='completed'`, `completed_at` → create `stock_movements` & decrement `inventory`.
- **Return** → create new sale with `sale_type='return'`, link to original via `sale_links`, reverse quantities (positive stock movement).
- **Manual stock adjust** → manager chooses location/item/variant, quantity delta, reason → write `stock_movements(type='adjust')`, update `inventory`.
- **Open/close shift** → insert `shifts` row on open with opening_cash; on close set closing_cash/expected_cash and close timestamp.
- **Install plugin** → add to `plugin_catalog` (if new) + `plugins` row with sha256 + runtime/entrypoint; load entries/settings/hooks; default inactive until trusted/activated; deletions cascade.

## Validation & Constraints
- Honor foreign key checks and the shared `(item_id XOR variant_id)` constraint across relevant tables.
- Prevent negative inventory on sale (unless override flag for managers).
- `is_active` flags used for soft delete in catalog, payment methods, plugins.
- Timestamps stored as ISO-8601 text (SQLite `datetime('now')` default is acceptable).

## Out of Scope / Later
- Promotions engine, coupons, mix-and-match, tiered pricing.
- Cost-of-goods accounting beyond storing `cost_price`.
- Multi-currency or tax-exempt modes.
- Device drivers beyond hooks exposed via plugins.
- Reporting/analytics, exports, or cloud sync.

## Acceptance / Completion Criteria
- All behaviors above can be expressed using only the existing schema; no new columns/tables.
- Spec remains consistent with `docs/data-model.md` terminology and money/quantity conventions.
- MVP can operate fully offline for catalog browse, sale completion, stock update, and plugin execution.
