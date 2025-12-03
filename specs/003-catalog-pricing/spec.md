# Catalog & Pricing Core (002a)

Status: Draft
Principles: local-first, offline, integer money, append-only migrations; no schema changes

## Purpose & Goals
- Deliver catalog CRUD for items/variants with barcodes/images/categories/brands/tax codes using existing tables.
- Enforce active/inactive filtering and barcode `(item_id XOR variant_id)` rules in UI and domain logic.
- Maintain price history append-only; resolve current price from latest row.

## Scope
- CRUD for `items`, `item_variants`, barcodes, images, categories, brands, tax codes.
- Search/browse by text/category/barcode; exclude inactive by default.
- Price history write/read helpers; guard integer minor unit handling.

## Non-Goals
- No promotions/advanced pricing, no schema/migration changes, no marketplace work.

## Functional Requirements
- Catalog CRUD uses existing schema tables only; no new columns/tables.
- Barcode uniqueness and `(item_id XOR variant_id)` enforced at app level.
- Price changes append new `price_history` rows; previous rows untouched.
- Active/inactive flags respected across queries and UI lists.

## Non-Functional Requirements
- Integer money; deterministic rounding consistent with constitution.
- Offline-capable (SQLite local store).
- Testable domain logic without UI/network.

## Acceptance Criteria
- Creating/updating/deactivating items/variants works and is covered by tests.
- Barcode rule enforcement errors when violated; tests cover happy/negative paths.
- Price resolution returns latest active price; append-only behavior verified.
- Inactive items/variants do not appear in sale/search flows.
