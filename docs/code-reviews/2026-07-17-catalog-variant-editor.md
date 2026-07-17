# Code review — catalog per-item variant/barcode editor

**Date:** 2026-07-17
**Branch:** `feat/variant-editor-panel`
**Ask (Farshid):** "when I click on a catalog [item], I should see all the
variants and be able to edit them; each variant can have a barcode." The old
UI only *listed* variants as muted text in the table; editing went through
blind pick-a-row mini-forms (price in raw minor units, no way to load an
existing variant's values, no barcode removal).

## What changed

- **New per-item editor panel** (`web/ui/partials/catalog_variants.html`,
  `GET /api/catalog/item-variants?item_id=`): clicking a catalog row loads a
  panel with the item's whole-item barcodes (add/remove, primary flag) and
  EVERY variant — name, SKU, price (entered in major units, converted
  client-side; the wire stays minor units), active checkbox, its barcodes
  (add/remove per variant). Inactive variants show dimmed and can be
  reactivated. Replaces the old "Variants" and "Barcodes" details forms.
- **Panel-aware mutations**: `/api/catalog/variant`, `/variant/deactivate`,
  `/barcode` re-render the panel when the request carries `panelItem`, with
  the items table riding along as an HTMX **out-of-band swap** (its
  variant/barcode summaries change too). Other callers keep the old
  table-only response.
- **New** `POST /api/catalog/barcode/delete` (+ `pos.RemoveBarcode`,
  `CatalogRepo.DeleteBarcode`) — mis-scans are routine corrections.
- **Repo**: `VariantsForItem` (all variants incl. inactive, with all
  barcodes), `BarcodesForItem`.
- **Bug fixed en route**: the variant handler read `isActive != "0"`, but an
  unchecked checkbox submits *nothing* → deactivating a variant via a form
  was impossible. The panel pairs the checkbox with a hidden `isActive=0`
  and the handler now scans all submitted values.
- i18n: 5 new keys × 4 locales (612 keys, guard green).

## Tests / verification

- `TestCatalogVariantsPanel` (real handlers + templates): renders both
  variants with SKUs and both barcode kinds; a panel save renames AND
  deactivates via the unchecked box; the response carries the OOB table;
  barcode delete works. Existing catalog page tests still green (the page
  now embeds the panel partial).
- **Live smoke** on a real running POS (scratch data dir): create item →
  add variant through the panel → attach per-variant barcode → verify it
  renders → deactivate via unchecked box → OOB fragment present.
- Full suite + `guard-data-access` + `guard-i18n` green.
