# Catalog & Pricing Notes (002a)

- Handlers added for create/update/deactivate of items and variants under `/api/catalog/item`, `/api/catalog/item/update`, `/api/catalog/item/deactivate`, `/api/catalog/variant`, `/api/catalog/variant/deactivate`.
- Catalog forms now include description, unit, weighed flag, category ID, brand ID, tax code, active flag; variants include cost price and active toggle.
- Barcode attachment continues to enforce `(item_id XOR variant_id)` with cross-table checks; reattaching to the same target is allowed, cross-target reassignment is blocked.
- Catalog list filters out inactive items; table shows ID, unit, weighed, category, brand, tax code, base price.
- Price history remains append-only; price resolution prefers latest open price_history row, otherwise base price for active items/variants.
- Tests cover handler flows (inactive filter, create+deactivate) and domain logic (barcode validation, price resolution, append-only history, item/variant update).
- No schema changes; UI paths remain in `web/ui/pages/catalog.html` and `web/ui/partials/catalog_table.html`.
