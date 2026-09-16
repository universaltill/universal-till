package data

// SellScreenCategoriesTabKey is the settings-table key for whether the
// sell screen renders ut-docs#2283's optional "Categories" tab (a grid of
// category tiles, each opening an item-picker modal for that category) —
// same "1"/"0", absent-reads-as-off, no GetOrCreate/seed shape as
// CatalogImportBarcodeFromSKUDefaultKey above: this is a purely
// presentational toggle (which tab renders), never behaviour the sale
// itself depends on, so a shop that never opens this setting keeps
// today's tab bar exactly as it is.
const SellScreenCategoriesTabKey = "sell_screen_categories_tab_enabled"
