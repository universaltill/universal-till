package corepos

// BasketLine represents a line in the basket.
type BasketLine struct {
	SKU          string
	Name         string
	Qty          float64
	PriceCents   int64
	LineDiscount int64
	LineTotal    int64
	ImageURL     string
	ItemID       string
	VariantID    string
	TaxRateBP    int
	IsWeighed    bool
}

// Basket represents the current basket state.
type Basket struct {
	Lines        []BasketLine
	Subtotal     int64
	Tax          int64
	Total        int64
	Discount     int64
	DiscountType string
	DiscountRaw  int64
	CustomerID   string
	CustomerName string
	ToastMessage string
}

// ...additional basket logic and methods can be moved here as needed...
