# Code review — catalog panel scroll + per-variant labels (2026-07-17)

Branch `fix/catalog-panel-length`. Farshid's field batch (v0.2.24
screenshots): (1) the catalog's right panel towers over the page; (2) label
printing should target VARIANTS; (3) variant-specific images → QUEUED as a
feature (storage per variant + upload UI + surfacing needs design, not a
quick patch).

## Panel
`.catalog-form` is now sticky with its own max-height + inner scroll — the
edit form/image/labels column can never exceed the viewport again,
regardless of how many sections are open.

## Variant labels (he's right: a label must scan as the variant)
- `CatalogRepo.GetVariantLabel`: composed "Item Variant" name, the
  VARIANT's price, its primary barcode (SKU fallback).
- `GET /api/catalog/variant-options?item_id` → JSON id/name of active
  variants (via the repo; envelope shape).
- Labels form gains a **Variant** select ("Whole item" default), populated
  on row-click; `POST /api/print/labels` prints the variant's label when
  `variant_id` is set, unchanged otherwise. i18n ×4.

## Tests
Repo-level: variant label composition, price, SKU→primary-barcode
precedence (schema note: variant_barcodes has no id column — barcode IS the
PK). Full pages/data/catalog suites + both guards green.
