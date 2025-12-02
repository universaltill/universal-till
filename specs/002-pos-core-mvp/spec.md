# POS Core MVP – Catalog, Sales, Inventory, Plugin Host

Author: Core team  
Status: Draft (MVP scope)  
Schema sources: `internal/db/migrations/001_init.sql`, `docs/data-model.md`  
Principles: local-first, offline-capable, integer money, append-only migrations

## Purpose & Goals
- Deliver a usable in-store POS that can run offline on modest hardware.
- Cover the three core domains: Catalog (items/variants/pricing), Inventory (stock per location), Sales (basket → payment → receipt), plus a minimal plugin host that matches existing schema contracts.
- Stay within the relational model already defined; no schema changes are allowed for this MVP.

## Vision: Universal Commerce Platform (Universal Till Core)

This project is a core building block for a larger Universal Commerce Platform.

┌─────────────────────────────────────────┐
│         UNIVERSAL TILL CORE             │
│     (Free, Open Source, Golang)         │
└─────────────────────────────────────────┘
					│
	────────────────┼────────────────
	│               │               │
┌───▼───┐      ┌───▼───┐      ┌───▼────┐
│ SELL  │      │ MANAGE│      │ EXPAND │
└───┬───┘      └───┬───┘      └───┬────┘
	│               │               │
	├─ In-store     ├─ Inventory    ├─ Marketplace plugins
	├─ Online       ├─ Employees    │   • eBay
	├─ Mobile       ├─ Reports      │   • Amazon
	└─ Delivery     └─ Accounting   │   • Shopify
									│   • Deliveroo
									│   • Uber Eats
									└─ • QuickBooks/Xero

Key platform notes:
- Plugins run as separate OS processes to enforce security isolation.
- Core communicates with plugins via IPC or gRPC; plugin processes expose a small RPC surface for lifecycle, hooks, and event handling.
- Plugins may be implemented in any language; the host enforces manifest checks (SHA256), declared capabilities, and runtime permission enforcement.
- A marketplace can host platform-specific plugin binaries (per OS/architecture) and manifests; installation verifies manifest checksum and records provenance.


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

### Rounding & Tax (precise rules)

- Rounding algorithm: round half-up to the nearest minor unit at the sale-line level.
	- For currencies with two decimal places (e.g., GBP), compute line tax and line totals in minor units (integers). When a calculation yields a fractional minor unit, round half-up (>= .5 → up).
	- Sale-level totals are the sum of rounded line values. Any residual rounding difference should be recorded in `sales.rounding` (signed integer minor units) so that `sales.total = sum(lines_unit_total) + sales.rounding`.
	- For weighted items (REAL quantity), compute unit_price * qty → intermediate minor-units (floating) → round half-up to nearest minor unit to persist line unit_total.

Examples:
- Item price £1.005 taxed 20% → intermediate tax = 0.201 → tax in minor units round half-up to 0 (pence) or 1 depending on exact intermediate; include example calculations in `data-model.md` tests.

### Negative Inventory Override

- Default policy: prevent sales that would create negative aggregated `inventory` for a location.
- Manager override: a user with role `manager` or `admin` may accept a negative inventory sale only when they perform an explicit override action in the UI. Override requirements:
	- Override must be performed by an authenticated `manager` or `admin` account.
	- The override action must create an `audit_log` entry with `actor_id`, `entity_type='sale'`, `action='negative_inventory_override'`, and `payload` containing `reason`, `line_ids`, and pre-sale inventory levels.
	- Overrides are applied per-sale (not per-line) but UI may present per-line warnings.

### Receipt Numbering

- Receipt numbers must be globally unique. Use the existing `sales.receipt_no` column (UNIQUE) and generate the next value transactionally without introducing new tables or migrations:
	- Allocate the next receipt number inside the sale-finalisation transaction using SQLite locking to prevent races (e.g., select max(receipt_no) → +1, or use `rowid`/`id` as the monotonic basis) and persist to `sales.receipt_no`.
	- Scope is global (not per-register) to avoid collisions and simplify reconciliation.
	- Add a concurrent test that simulates multiple checkout processes to prove uniqueness and monotonicity with the no-new-table approach.

### Event Dispatch Semantics

- Plugin events are dispatched synchronously by default (the host calls plugin handlers during the triggering transaction) but must not compromise core data integrity:
	- If a plugin handler returns an error during a non-critical event (e.g., `sale.viewed`), log the error, audit it, and continue the primary flow.
	- For critical events that must be enforced (e.g., payment authorization hooks), the plan must explicitly mark them as blocking and document rollback semantics; otherwise the default is non-blocking with audit.
	- The host must protect critical DB transactions: do not allow plugin errors to leave the DB in an inconsistent state. Prefer invoking non-essential plugin side-effects after the DB transaction commits, or use compensating actions.

### Plugin Permission Summary

- Plugins must declare capabilities in `plugin_permissions` and the host enforces them. Minimal contract:
	- `capability`: string (e.g., `payment.capture`, `inventory.adjust`, `sale.read`).
	- `scope`: `global|register|sale` (where applicable).
	- Host enforcement points: at load time (deny plugin with missing required platform capabilities), and at runtime before executing privileged actions (deny and audit if capability missing).
	- Failure mode: Deny the action, return clear error to caller, and emit `audit_log` with `action='plugin_permission_denied'` and payload noting plugin id, attempted action, and actor/context.


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

## Platform-level Requirements (from Universal Commerce vision)

- **FR-001**: System MUST support offline-first checkout for in-store sales (scan → basket → payment → receipt) with persisted `sales` and `stock_movements`.
- **FR-002**: System MUST persist plugin manifests and installed plugin metadata (`plugin_catalog`, `plugins`, `plugin_entries`) and enforce declared plugin permissions at runtime.
- **FR-003**: System MUST run plugins as separate OS processes and provide an IPC/gRPC protocol for event delivery and RPC (plugin lifecycle, event hooks, data sync).
- **FR-004**: System MUST treat monetary values as integer minor units and provide deterministic rounding (see `Rounding & Tax` section above).
- **FR-005**: System MUST provide role-based actions (cashier, manager, admin) and require authorization for overrides (e.g., negative inventory override) with audit trails.
- **FR-006**: System MUST ensure safe plugin installation: verify manifest checksum (SHA256), record provenance, and set default trust level `untrusted` until reviewed.
- **FR-007**: System SHOULD provide a marketplace integration flow allowing operators to install prebuilt plugin binaries for supported platforms and list compatible binaries per OS/arch.

## Success Criteria (from Universal Commerce vision)

- **SC-001**: Perform an end-to-end in-store sale (scan→payment→receipt) on a cold device with `UT_STORE=sqlite` within 5 seconds (measured on target hardware in smoke tests).
- **SC-002**: Marketplace plugin installation workflow completes and plugin appears in UI with manifest verified (SHA256) and permissions enforced (automated test).
- **SC-003**: 100% of installed plugins run in isolated processes; a plugin crash does not corrupt core DB (verified by integration test replaying events).
- **SC-004**: Rounding deterministic tests pass (line-level half-up rounding) with `sales.rounding` accounting for residuals (unit tests).
