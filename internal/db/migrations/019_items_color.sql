-- ut-docs#1901: a fixed tile-color swatch an operator can set on an item,
-- rendered as a solid tile background for photo-less items on the sale
-- screen (buttons.html's product-tile, via ButtonVM.Color/--tile-color)
-- and as a small dot in the catalog list. Nullable: NULL/'' means "no
-- color set", same convention as the other optional item columns
-- (category_id, brand_id, tax_code_id). The stored value is always one of
-- catalogtypes.ItemColors()' fixed hex swatches -- validated server-side
-- (internal/pages/catalog's validateLookups) before it ever reaches this
-- column, since it flows into a CSS custom property downstream.
ALTER TABLE items ADD COLUMN color TEXT;
