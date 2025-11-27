# Tasks: POS Core MVP

**Input**: `specs/pos-core-mvp/` (spec, plan, research, data-model, quickstart, contracts)  
**Prerequisites**: No schema changes; SQLite (`UT_STORE=sqlite`); responsive web UI

## Phase 0: Setup / Foundation

- [ ] T001 Ensure `PRAGMA foreign_keys = ON` in DB bootstrap (`internal/db`); add guard if missing.
- [ ] T002 Validate money/quantity helpers handle integer minor units and REAL for weighed items (`internal/pos` or shared helpers); add rounding rules for tax inclusive/exclusive.
- [ ] T003 Add/confirm price resolution uses latest `price_history` row (open-ended) for item/variant (`internal/pos` pricing logic).
- [ ] T004 Wrap sale persistence in DB transaction utilities to prevent orphan `sale_lines`/`payments` (`internal/pos`).

## Phase 1: User Story 1 – Complete a sale offline (P1)

- [ ] T101 [P] Implement barcode/SKU lookup with fallback search (`internal/pos` + `internal/pages` handlers); prefer primary barcode tables.
- [ ] T102 [P] Basket operations: add/edit/remove lines (incl. weighed) capturing snapshots (name, sku/barcode, tax_rate_bp, unit_price, totals) in `sale_lines`; exclude `is_active=0` catalog (`internal/pos`, `internal/pages`, `web/` templates).
- [ ] T103 Tax calc respects `settings` inclusive/exclusive with integer-safe rounding; persist line tax and sale totals (`sales.subtotal`, `tax_total`, `total`, optional rounding) (`internal/pos`).
- [ ] T104 Discounts: persist sale- and line-level discounts in `sale_discounts` (fixed/percent) and roll into `sales.discount_total` (`internal/pos`).
- [ ] T105 Payments: support multiple tenders using seeded `payment_methods`; persist `payments` with amount/currency/reference/change_given/paid_at; block completion until covered (`internal/pos`, `internal/pages`).
- [ ] T106 Completion flow: generate unique `receipt_no`, set `sales.status=completed`/`completed_at`, rollback on partial/failed tender (`internal/pos`).
- [ ] T107 Stock movements: on completion create `stock_movements(type='sale')` per line with negative qty and `sale_line_id`; update `inventory` aggregates (`internal/pos`).
- [ ] T108 Receipt payload: include lines, tax, discounts, payments, totals; printable/exportable via existing templates (`web/`).
- [ ] T109 Audit: record sale lifecycle (create, complete, void/park) with actor/payload in `audit_log` (`internal/pos` logging).

## Phase 2: User Story 2 – Manage catalog & pricing (P1)

- [ ] T201 [P] CRUD for items/variants with barcodes, images, category, brand, tax code; enforce `(item_id XOR variant_id)` on barcodes; honor `is_active` (`internal/pos`, `internal/pages`, `web/`).
- [ ] T202 [P] Price changes append new `price_history` row with timestamps; previous rows untouched (`internal/pos`).
- [ ] T203 [P] Ensure inactive catalog records are hidden from sale selection and searches; add UI guard (`internal/pages`, `web/`).

## Phase 3: User Story 3 – Maintain inventory accuracy (P2)

- [ ] T301 [P] Stock receipts/adjustments: create `stock_movements` entries and update `inventory` aggregates per `stock_location` (`internal/pos`).
- [ ] T302 [P] Returns: model as new sales with `sale_type='return'`, link via `sale_links`, post positive movements; receipt reflects link (`internal/pos`, `internal/pages`, `web/`).
- [ ] T303 Negative inventory policy: block by default; allow explicit manager override with audit entry (`internal/pos`).

## Phase 4: User Story 4 – Basic plugin host operability (P3)

- [ ] T401 [P] Persist plugin manifest data into `plugins`, `plugin_entries`, `plugin_settings`, `plugin_permissions`, `plugin_hooks` per contracts (`internal/plugins`).
- [ ] T402 [P] Render plugin entries (page/button/popup/customer_facing/receipt_template) in UI shell and enforce declared permissions/capabilities (`internal/plugins`, `internal/pages`, `web/`).
- [ ] T403 Audit plugin enable/disable/install actions in `audit_log` with actor/payload (`internal/plugins`).

## Phase 5: Tests & Validation

- [ ] T501 Unit tests for pricing/tax rounding, price resolution, and discount application (`internal/pos` tests).
- [ ] T502 Unit tests for stock movement generation and inventory aggregation (`internal/pos` tests).
- [ ] T503 Unit/handler tests for plugin permission enforcement (deny/allow) (`internal/plugins` tests).
- [ ] T504 Manual quickstart run with SQLite (`quickstart.md` flow) covering scan→cart→payment→receipt and a return; verify DB rows.

## Phase 6: Polish

- [ ] T601 Update any user-facing docs/help in `docs/` or `web/` if UI changes materially.
- [ ] T602 Review logging to avoid sensitive data (payments) and ensure clarity for audits.
