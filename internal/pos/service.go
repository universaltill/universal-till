package pos

type PriceResolver interface {
	Resolve(code string) (BasketLine, bool)
}

type Service struct {
	cfg      Config
	basket   Basket
	resolver PriceResolver
	tax      TaxEngine
	lines    []BasketLine // persisted after completion
	// discount can be fixed amount or percentage basis points (1% = 100)
	discountType      string
	discountValue     int64
	discountPercentBP int64
	customerID        string
	customerName      string
}

type Config struct {
	TaxInclusive       bool
	TaxRateBasisPoints int // e.g. 2000 = 20.00%
}

func NewServiceWithResolver(cfg Config, r PriceResolver) *Service {
	tax := BasisPointsTaxEngine{RateBasisPoints: cfg.TaxRateBasisPoints, Inclusive: cfg.TaxInclusive}
	if cfg.TaxRateBasisPoints == 0 {
		// default to 20% if not provided
		tax.RateBasisPoints = 2000
	}
	return &Service{cfg: cfg, resolver: r, tax: tax}
}

type BasketLine struct {
	SKU          string  `json:"sku"`
	Name         string  `json:"name"`
	Qty          float64 `json:"qty"`
	PriceCents   int64   `json:"priceCents"`
	LineDiscount int64   `json:"lineDiscount,omitempty"`
	LineTotal    int64   `json:"lineTotal,omitempty"`
	ImageURL     string  `json:"imageUrl,omitempty"`
	ItemID       string  `json:"-"`
	VariantID    string  `json:"-"`
	TaxRateBP    int     `json:"-"`
	IsWeighed    bool    `json:"-"`
}

type Basket struct {
	Lines        []BasketLine `json:"lines"`
	Subtotal     int64        `json:"subtotal"`
	Tax          int64        `json:"tax"`
	Total        int64        `json:"total"`
	Discount     int64        `json:"discount"`
	DiscountType string       `json:"discountType,omitempty"` // amount|percent
	DiscountRaw  int64        `json:"discountRaw,omitempty"`  // minor units or basis points
	CustomerID   string       `json:"customerId,omitempty"`
	CustomerName string       `json:"customerName,omitempty"`
	ToastMessage string       `json:"toastMessage,omitempty"`
}

func (s *Service) Scan(code string) (*Basket, error) {
	return s.ScanQty(code, 1)
}

func (s *Service) ScanQty(code string, qty float64) (*Basket, error) {
	if qty <= 0 {
		qty = 1
	}
	item, ok := s.resolver.Resolve(code)
	if !ok {
		return &s.basket, nil
	}
	item.Qty = qty
	// merge with existing line if same SKU/item/variant
	merged := false
	for i := range s.lines {
		if s.lines[i].SKU == item.SKU || (s.lines[i].ItemID == item.ItemID && s.lines[i].VariantID == item.VariantID) {
			s.lines[i].Qty += qty
			merged = true
			break
		}
	}
	if !merged {
		s.lines = append(s.lines, item)
	}
	s.recomputeTotals()
	return &s.basket, nil
}

func (s *Service) recomputeTotals() {
	var sub int64
	for i := range s.lines {
		l := &s.lines[i]
		lineBase := AmountForQuantity(l.PriceCents, l.Qty)
		lineNet := lineBase - l.LineDiscount
		if lineNet < 0 {
			lineNet = 0
		}
		l.LineTotal = lineNet
		sub += lineNet
	}
	s.basket.Lines = append([]BasketLine{}, s.lines...)
	s.basket.Subtotal = sub
	discount := int64(0)
	switch s.discountType {
	case "percent":
		if s.discountPercentBP > 0 && sub > 0 {
			// round to nearest minor unit
			discount = (sub*int64(s.discountPercentBP) + 9999) / 10000
		}
	default:
		discount = s.discountValue
	}
	if discount < 0 {
		discount = 0
	}
	s.basket.Discount = discount
	s.basket.DiscountType = s.discountType
	if s.discountType == "percent" {
		s.basket.DiscountRaw = s.discountPercentBP
	} else {
		s.basket.DiscountRaw = s.discountValue
	}
	s.basket.CustomerID = s.customerID
	s.basket.CustomerName = s.customerName
	tax, total := int64(0), sub
	if s.tax != nil {
		tax, total = s.tax.Compute(sub)
	}
	s.basket.Tax = tax
	total -= discount
	if total < 0 {
		total = 0
	}
	s.basket.Total = total
}

func (s *Service) Tender(amount int64, method string) (map[string]any, error) {
	// reset basket for demo
	s.basket = Basket{}
	s.lines = nil
	return map[string]any{"status": "ok", "method": method, "amount": amount}, nil
}

// Lines returns a copy of the current basket lines.
func (s *Service) Lines() []BasketLine {
	return append([]BasketLine{}, s.lines...)
}

// Basket returns the current basket snapshot.
func (s *Service) Basket() Basket {
	s.recomputeTotals()
	return s.basket
}

// Remove removes a line by SKU (or item/variant ID fallback) and recomputes totals.
func (s *Service) Remove(sku string) {
	filtered := s.lines[:0]
	for _, l := range s.lines {
		if l.SKU == sku {
			continue
		}
		filtered = append(filtered, l)
	}
	s.lines = filtered
	s.recomputeTotals()
}

// Reset clears basket and lines after completion.
func (s *Service) Reset() {
	s.basket = Basket{}
	s.lines = nil
	s.discountType = ""
	s.discountValue = 0
	s.discountPercentBP = 0
	s.customerID = ""
	s.customerName = ""
	s.basket.CustomerID = ""
	s.basket.CustomerName = ""
}

// UpdateLine sets qty/discount for a given SKU (or item/variant match) and recomputes totals.
func (s *Service) UpdateLine(code string, qty float64, discount int64) {
	if qty < 0 {
		qty = 0
	}
	for i := range s.lines {
		if s.lines[i].SKU == code || (s.lines[i].ItemID == code || s.lines[i].VariantID == code) {
			s.lines[i].Qty = qty
			s.lines[i].LineDiscount = discount
			if s.lines[i].Qty == 0 {
				s.Remove(s.lines[i].SKU)
				return
			}
			s.recomputeTotals()
			return
		}
	}
}

// SetDiscount sets the sale-level discount (minor units) and recomputes totals.
func (s *Service) SetDiscount(discount int64) {
	if discount < 0 {
		discount = 0
	}
	s.discountType = "amount"
	s.discountValue = discount
	s.discountPercentBP = 0
	s.recomputeTotals()
}

// SaleDiscount returns the current sale-level discount.
func (s *Service) SaleDiscount() int64 {
	s.recomputeTotals()
	return s.basket.Discount
}

// SetDiscountPercent sets a sale-level percentage discount (basis points, 1% = 100).
func (s *Service) SetDiscountPercent(bp int64) {
	if bp < 0 {
		bp = 0
	}
	s.discountType = "percent"
	s.discountPercentBP = bp
	s.discountValue = 0
	s.recomputeTotals()
}

// CustomerID returns the current customer attached to the basket.
func (s *Service) CustomerID() string {
	return s.customerID
}

// SetCustomerID attaches the basket to a customer (or clears when empty).
func (s *Service) SetCustomerID(customerID string) {
	s.customerID = customerID
	s.basket.CustomerID = customerID
}

// SetCustomer sets both id and name for the basket.
func (s *Service) SetCustomer(id, name string) {
	s.customerID = id
	s.customerName = name
	s.basket.CustomerID = id
	s.basket.CustomerName = name
}

// // simple in-memory resolver
// type mapResolver map[string]BasketLine

// func (m mapResolver) Resolve(code string) (BasketLine, bool) {
// 	v, ok := m[code]
// 	return v, ok
// }
