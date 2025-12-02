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
