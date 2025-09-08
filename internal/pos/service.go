package pos

type Service struct {
    cfg Config
}

type Config struct{ TaxInclusive bool }

func NewService(cfg Config) *Service { return &Service{cfg: cfg} }

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
    return &Basket{Subtotal: 0, Tax: 0, Total: 0}, nil
}

func (s *Service) Tender(amount int64, method string) (map[string]any, error) {
    return map[string]any{"status": "ok", "method": method, "amount": amount}, nil
}
