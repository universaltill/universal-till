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
