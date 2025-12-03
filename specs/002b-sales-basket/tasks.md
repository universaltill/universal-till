# Tasks: Sales Flow & Basket (002b)

## Phase 1: Helpers
- [x] SB-101 Add rounding/tax helper (half-up) covering inclusive/exclusive settings; table-driven tests.
- [x] SB-102 Ensure catalog selection filters out inactive items/variants; add guard tests.

## Phase 2: Basket Operations
- [x] SB-201 Implement add/edit/remove for basket lines (incl. weighed items) with snapshots stored on `sale_lines` structs.
- [x] SB-202 Support sale- and line-level discounts; ensure totals reflect discounts.

## Phase 3: Status & Audit
- [x] SB-301 Implement park flow (status change, persistence, audit entry).
- [x] SB-302 Implement void flow (status change, audit entry, safe cleanup).

## Phase 4: Receipts & UI
- [x] SB-401 Ensure receipt payload includes lines/discounts/totals; render via existing templates (`web/`).
- [x] SB-402 Handler/HTMX tests for basket and park/void actions.

## Phase 5: Promotions & Customer Linking
- [ ] SB-501 Extend promotions to support `type` (amount|percent) and value handling; apply math correctly per type.
- [ ] SB-502 Add customer scan/link support so basket carries customer_id for targeted promotions.
- [ ] SB-503 Update promo scan to honor type/date window/customer targeting; add tests covering amount vs percent and customer-limited promos.
- [ ] SB-504 Surface promotion line/summary in basket/receipt with type-aware display.
