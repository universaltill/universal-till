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
	Lines    []BasketLine `json:"lines"`
	Subtotal int64        `json:"subtotal"`
	Tax      int64        `json:"tax"`
	Total    int64        `json:"total"`
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
	tax, total := int64(0), sub
	if s.tax != nil {
		tax, total = s.tax.Compute(sub)
	}
	s.basket.Tax = tax
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

// // simple in-memory resolver
// type mapResolver map[string]BasketLine

// func (m mapResolver) Resolve(code string) (BasketLine, bool) {
// 	v, ok := m[code]
// 	return v, ok
// }
