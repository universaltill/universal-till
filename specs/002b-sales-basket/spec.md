# Sales Flow & Basket (002b)

Status: Draft
Principles: offline-first, integer money, append-only migrations (no schema changes)

## Purpose & Goals
- Implement basket operations (add/edit/remove, weighed items) with sale-line snapshots.
- Handle tax/discounts deterministically and support park/void flows.
- Render receipt payloads consistent with existing templates.

## Scope
- Basket management and sale lifecycle up to pre-payment.
- Tax-inclusive/exclusive calculations, discounts (line/sale).
- Park/void flows with audit logging hooks.

## Non-Goals
- Payment capture, stock movements, plugin hooks (handled in other slices).

## Functional Requirements
- Sale lines capture name, SKU/barcode, tax rate bp, unit_price, discounts, totals at time of addition.
- Basket operations exclude inactive catalog entries.
- Park and void actions change sale status and record audit events.
- Deterministic rounding (half-up) per constitution.
- Promotions/coupons stored in `promotions` table with fields: code, type (`amount|percent`), value (minor units or basis points), optional customer_id, validity window; applied via barcode scan and must respect type math.
- Customer barcode/ID scan should attach a customer to the basket for promo eligibility checks.
- Handlers should delegate DB access (customer lookup, promo lookup, register/payment/user helpers) to a small POS repo to keep HTTP layer thin and testable.
- Helper scripts (available anytime):
  - `scripts/run_migrations`: apply DB migrations.
  - `scripts/smoke_quickstart`: quick sanity pass over POS/UI.
  - `scripts/test-plugin`: basic plugin harness check.

## Acceptance Criteria
- Basket add/edit/remove works for weighted and integer quantities; tests cover rounding and discounts.
- Park/void flows persist status changes and emit audit records.
- Receipt payload includes lines/discounts/totals (including promotions) and renders via existing templates.
- Promotions honor type (`amount|percent`), date window, and optional customer targeting; customer scan links basket to promotions.
