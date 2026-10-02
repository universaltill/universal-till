package data

// CatalogPrePackUnitPriceEnabledKey is the settings-table key holding
// whether a pre-packed item's shelf label shows its unit price (per kg /
// per litre / per item), computed from its net quantity — ut-docs#3391,
// Price Marking Order 2004 as amended from 6 April 2026. "1" = show, "0" or
// absent = don't. Off by default: shops of 280 m² or less are exempt from
// unit pricing pre-packs, so it is the shop's own call. A weighed item's
// per-kg label line (ut-docs#3343) never reads this key. Same no-seed shape
// as CatalogImportBarcodeFromSKUDefaultKey: absent reads as off.
const CatalogPrePackUnitPriceEnabledKey = "catalog_pre_pack_unit_price_enabled"
