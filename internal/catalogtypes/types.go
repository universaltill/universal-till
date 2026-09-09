package catalogtypes

type ItemInput struct {
	ID          string
	SKU         string
	Name        string
	BasePrice   int64
	Unit        string
	CategoryID  *string
	BrandID     *string
	TaxCodeID   *string
	IsWeighed   bool
	Description string
	IsActive    bool
	// IsSampleData marks rows inserted by the opt-in demo catalogue seed
	// (ut-docs#539) so the UI can badge them.
	IsSampleData bool
	// StockUntracked marks an item that carries no stock movements or
	// inventory row at all (ut-docs#1850), independent of the shop-wide
	// AllowNegativeInventory switch. Named in the inverted sense so its
	// Go zero value (false) means "tracked" — see
	// 013_items_stock_untracked.sql for why that direction matters.
	StockUntracked bool
	// Color is this item's tile swatch (ut-docs#1901) — one of
	// ItemColors()' fixed hex values, e.g. "#0f172a", or "" for none.
	// Renders as the sale-screen tile's solid background for a photo-less
	// item (ButtonVM.Color/--tile-color, buttons.html's product-tile) and
	// as a small dot in the catalog list. Always validated against
	// ItemColors() before it reaches this field on the write path — see
	// ValidItemColor and internal/pages/catalog's validateLookups — since
	// it flows into a CSS custom property downstream and an arbitrary
	// string is not safe to trust there.
	Color string
}

// ItemColor is one swatch in the fixed item-color palette (ut-docs#1901).
// Key is a short, stable identifier used only for its i18n name lookup
// (I18nKey) — the value actually stored on an item and rendered into CSS
// is Hex, same shape as catimport.BuiltinIcon (Key/Path/I18nKey) that the
// built-in image picker already uses.
type ItemColor struct {
	Key     string
	Hex     string
	I18nKey string
}

// ItemColors returns the fixed, curated tile-color palette in display
// order — the SAME literal list a template ranges over to render the
// swatch picker (catalog.html's #item-color-grid, mirroring how
// .BuiltinIcons/catimport.BuiltinIcons already does for the built-in icon
// grid) and ValidItemColor checks a submitted value against. Defined once,
// here, so the picker UI and the server-side allowlist can never drift
// apart. Deliberately NOT a free color picker: a raw, unvalidated value
// would flow straight into a CSS custom property downstream
// (product-tile's --tile-color, catalog_row.html's data-color) — this
// fixed set is a real security control (no delimiter/parenthesis/
// semicolon a value could use to break out of that context can ever reach
// storage), not just a design choice. Eight curated, muted/dark tones
// consistent with the "Slate" navy/muted-accent design direction (#146) —
// deliberately avoiding a saturated primary red/green swatch.
func ItemColors() []ItemColor {
	return []ItemColor{
		{Key: "slate", Hex: "#0f172a", I18nKey: "catalog.color.slate"},
		{Key: "indigo", Hex: "#4338ca", I18nKey: "catalog.color.indigo"},
		{Key: "teal", Hex: "#0f766e", I18nKey: "catalog.color.teal"},
		{Key: "amber", Hex: "#b45309", I18nKey: "catalog.color.amber"},
		{Key: "rose", Hex: "#be185d", I18nKey: "catalog.color.rose"},
		{Key: "violet", Hex: "#7c3aed", I18nKey: "catalog.color.violet"},
		{Key: "sky", Hex: "#0369a1", I18nKey: "catalog.color.sky"},
		{Key: "stone", Hex: "#57534e", I18nKey: "catalog.color.stone"},
	}
}

// ValidItemColor reports whether hex is one of ItemColors()' fixed swatch
// values, or "" (meaning "no color set" — always allowed, it's how a
// color is cleared). Any other value — including a syntactically-valid
// hex color that just isn't in the palette — is rejected: see ItemColors'
// own doc comment for why this is a real allowlist, not merely a UX
// nicety.
func ValidItemColor(hex string) bool {
	if hex == "" {
		return true
	}
	for _, c := range ItemColors() {
		if c.Hex == hex {
			return true
		}
	}
	return false
}

type VariantInput struct {
	ID        string
	ItemID    string
	SKU       string
	Name      string
	Price     int64
	CostPrice *int64
	IsActive  bool
}

type BarcodeInput struct {
	Barcode     string
	ItemID      string
	VariantID   string
	BarcodeType string
	IsPrimary   bool
	ForVariant  bool
}
