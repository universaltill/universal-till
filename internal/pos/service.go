package pos

type PriceResolver interface {
	Resolve(code string) (BasketLine, bool)
}

type Service struct {
	cfg      Config
	basket   Basket
	resolver PriceResolver
	tax      TaxEngine
}

type Config struct{ TaxInclusive bool }

func NewServiceWithResolver(cfg Config, r PriceResolver) *Service {
	return &Service{cfg: cfg, resolver: r, tax: PercentTaxEngine{RatePercent: 20, Inclusive: cfg.TaxInclusive}}
}

// Backward compat for tests/demos
func NewService(cfg Config) *Service {
	price := map[string]BasketLine{
		"A": {SKU: "A", Name: "Coffee", Qty: 1, PriceCents: 250},
		"B": {SKU: "B", Name: "Tea", Qty: 1, PriceCents: 200},
		"C": {SKU: "C", Name: "Cake", Qty: 1, PriceCents: 350},
	}
	return &Service{cfg: cfg, resolver: mapResolver(price)}
}

type BasketLine struct {
	SKU        string `json:"sku"`
	Name       string `json:"name"`
	Qty        int    `json:"qty"`
	PriceCents int64  `json:"priceCents"`
	ImageURL   string `json:"imageUrl,omitempty"`
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

func (s *Service) ScanQty(code string, qty int) (*Basket, error) {
	if qty <= 0 {
		qty = 1
	}
	item, ok := s.resolver.Resolve(code)
	if !ok {
		return &s.basket, nil
	}
	// increment if exists
	found := false
	for i := range s.basket.Lines {
		if s.basket.Lines[i].SKU == item.SKU {
			s.basket.Lines[i].Qty += qty
			found = true
			break
		}
	}
	if !found {
		item.Qty = qty
		s.basket.Lines = append(s.basket.Lines, item)
	}
	// totals
	var sub int64
	for _, l := range s.basket.Lines {
		sub += int64(l.Qty) * l.PriceCents
	}
	s.basket.Subtotal = sub
	tax, total := int64(0), sub
	if s.tax != nil {
		tax, total = s.tax.Compute(sub)
	}
	s.basket.Tax = tax
	s.basket.Total = total
	return &s.basket, nil
}

func (s *Service) Tender(amount int64, method string) (map[string]any, error) {
	// reset basket for demo
	s.basket = Basket{}
	return map[string]any{"status": "ok", "method": method, "amount": amount}, nil
}

// simple in-memory resolver
type mapResolver map[string]BasketLine

func (m mapResolver) Resolve(code string) (BasketLine, bool) {
	v, ok := m[code]
	return v, ok
}
