# Tasks: Catalog & Pricing Core (002a)

## Phase 1: Foundations
- [x] CA-101 Confirm FK/constraint config and add `(item_id XOR variant_id)` barcode validator in domain layer (`internal/pos`).
- [x] CA-102 Add helper for price resolution (latest `price_history` row) using integer minor units; table-driven tests.

## Phase 2: CRUD & UI
- [x] CA-201 Implement item/variant create/update/deactivate handlers and templates; support barcodes/images/category/brand/tax code (`internal/pages`, `web/`).
- [x] CA-202 Enforce barcode uniqueness + XOR rule at handler/service layer with clear errors; tests for duplicate/invalid cases.
- [x] CA-203 Ensure inactive items/variants are filtered from search/quick-add; add regression tests.

## Phase 3: Price History
- [x] CA-301 Append-only price update path that writes new `price_history` rows; prevent in-place edits; tests for append-only behavior.

## Phase 4: Testing & Docs
- [x] CA-401 Unit tests for CRUD helpers and barcode validation (`internal/pos` tests).
- [x] CA-402 Handler/HTMX tests for create/update/deactivate flows.
- [ ] CA-403 Update `specs/002a-catalog-pricing/data-model.md` or notes as needed; add quickstart notes if UI paths change.
