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
