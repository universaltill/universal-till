package pos

type Service struct {
	cfg    Config
	basket Basket
	price  map[string]BasketLine
}

type Config struct{ TaxInclusive bool }

func NewService(cfg Config) *Service {
	// demo price list matches default buttons
	price := map[string]BasketLine{
		"A": {SKU: "A", Name: "Coffee", Qty: 1, PriceCents: 250},
		"B": {SKU: "B", Name: "Tea", Qty: 1, PriceCents: 200},
		"C": {SKU: "C", Name: "Cake", Qty: 1, PriceCents: 350},
	}
	return &Service{cfg: cfg, price: price}
}

type BasketLine struct {
	SKU        string `json:"sku"`
	Name       string `json:"name"`
	Qty        int    `json:"qty"`
	PriceCents int64  `json:"priceCents"`
}

type Basket struct {
	Lines    []BasketLine `json:"lines"`
	Subtotal int64        `json:"subtotal"`
	Tax      int64        `json:"tax"`
	Total    int64        `json:"total"`
}

func (s *Service) Scan(code string) (*Basket, error) {
	// find or add
	item, ok := s.price[code]
	if !ok {
		// unknown code: ignore for now
		return &s.basket, nil
	}
	// increment if exists
	found := false
	for i := range s.basket.Lines {
		if s.basket.Lines[i].SKU == item.SKU {
			s.basket.Lines[i].Qty++
			found = true
			break
		}
	}
	if !found {
		s.basket.Lines = append(s.basket.Lines, item)
	}
	// recompute totals
	var sub int64
	for _, l := range s.basket.Lines {
		sub += int64(l.Qty) * l.PriceCents
	}
	s.basket.Subtotal = sub
	if s.cfg.TaxInclusive {
		s.basket.Tax = 0
		s.basket.Total = sub
	} else {
		s.basket.Tax = sub / 5 // demo 20% VAT-ish for demo UI
		s.basket.Total = sub + s.basket.Tax
	}
	return &s.basket, nil
}

func (s *Service) Tender(amount int64, method string) (map[string]any, error) {
	// reset basket for demo
	s.basket = Basket{}
	return map[string]any{"status": "ok", "method": method, "amount": amount}, nil
}
