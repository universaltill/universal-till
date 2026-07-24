package pos

import (
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

type PriceResolver interface {
	Resolve(code string) (BasketLine, bool)
}

type Service struct {
	cfg       Config
	basket    Basket
	resolver  PriceResolver
	tax       TaxEngine
	lines     []BasketLine // persisted after completion
	scanCache map[string]BasketLine
	// discount can be fixed amount or percentage basis points (1% = 100)
	discountType      string
	discountValue     money.Money // fixed discount amount (minor units)
	discountPercentBP int64       // percentage discount as basis points (a rate, not money)
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
	return &Service{cfg: cfg, resolver: r, tax: tax, scanCache: map[string]BasketLine{}}
}

// SetConfig swaps the tax configuration IN PLACE, keeping the basket.
// Background config refreshes (e.g. the LAN-sync pull) must use this
// rather than replacing the service — a replacement engine is empty and
// would silently discard a sale in progress.
func (s *Service) SetConfig(cfg Config) {
	tax := BasisPointsTaxEngine{RateBasisPoints: cfg.TaxRateBasisPoints, Inclusive: cfg.TaxInclusive}
	if cfg.TaxRateBasisPoints == 0 {
		tax.RateBasisPoints = 2000
	}
	s.cfg, s.tax = cfg, tax
	s.recomputeTotals()
}

// Config returns the service's current tax configuration.
func (s *Service) Config() Config { return s.cfg }

type BasketLine struct {
	LineKey      string                  `json:"lineKey,omitempty"` // stable per-line id, assigned when a line is first appended (ADR-0020) — the only safe way to address ONE line once modifiers let several share a SKU
	SKU          string                  `json:"sku"`
	Name         string                  `json:"name"`
	Qty          float64                 `json:"qty"`
	PriceCents   money.Money             `json:"priceCents"` // effective unit price: base + sum(Modifiers deltas)
	LineDiscount money.Money             `json:"lineDiscount,omitempty"`
	LineTotal    money.Money             `json:"lineTotal,omitempty"`
	ImageURL     string                  `json:"imageUrl,omitempty"`
	ItemID       string                  `json:"-"`
	VariantID    string                  `json:"-"`
	TaxRateBP    int                     `json:"-"`
	IsWeighed    bool                    `json:"-"`
	Modifiers    []data.SelectedModifier `json:"modifiers,omitempty"` // ADR-0020: chosen customizations, already folded into PriceCents
}

// ModifierSignature is a stable key for two lines' modifier selections —
// used to decide whether adding the same item again merges quantity into
// an existing line or starts a new one. Two identical selections (same
// options, any order) merge; anything else is a distinct line, since they
// price and print differently.
func (l BasketLine) ModifierSignature() string {
	if len(l.Modifiers) == 0 {
		return ""
	}
	ids := make([]string, len(l.Modifiers))
	for i, m := range l.Modifiers {
		ids[i] = m.OptionID
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

type Basket struct {
	Lines        []BasketLine `json:"lines"`
	Subtotal     money.Money  `json:"subtotal"`
	Tax          money.Money  `json:"tax"`
	Total        money.Money  `json:"total"`
	Discount     money.Money  `json:"discount"`
	DiscountType string       `json:"discountType,omitempty"` // amount|percent
	DiscountRaw  int64        `json:"discountRaw,omitempty"`  // minor units or basis points (kept raw)
	CustomerID   string       `json:"customerId,omitempty"`
	CustomerName string       `json:"customerName,omitempty"`
	ToastMessage string       `json:"toastMessage,omitempty"`
}

func (s *Service) Scan(code string) (*Basket, error) {
	return s.ScanQty(code, 1)
}

// ResolveBase resolves code to its plain, pre-modifier BasketLine via the
// configured PriceResolver WITHOUT adding it to the basket (ADR-0020) — the
// modifier-selection step needs the item's base price/name before the
// operator has chosen any customization, to build the picker and then call
// AddLineWithModifiers once they have.
func (s *Service) ResolveBase(code string) (BasketLine, bool) {
	code = strings.TrimSpace(code)
	if s.resolver == nil || code == "" {
		return BasketLine{}, false
	}
	return s.resolver.Resolve(code)
}

func (s *Service) ScanQty(code string, qty float64) (*Basket, error) {
	b, _ := s.scanQty(code, qty)
	return b, nil
}

func (s *Service) ScanQtyWithResult(code string, qty float64) (*Basket, bool, error) {
	b, found := s.scanQty(code, qty)
	return b, found, nil
}

func (s *Service) scanQty(code string, qty float64) (*Basket, bool) {
	if qty <= 0 {
		qty = 1
	}
	code = strings.TrimSpace(code)
	var resolved BasketLine
	var ok bool

	if code != "" {
		if cached, found := s.scanCache[code]; found {
			cached.Qty = qty
			s.mergeResolved(cached)
			return &s.basket, true
		}
	}

	if s.resolver != nil && code != "" {
		resolved, ok = s.resolver.Resolve(code)
	}
	if ok {
		resolved.Qty = qty
		s.mergeResolved(resolved)
		s.cacheScan(code, resolved)
		return &s.basket, true
	}

	if code != "" {
		if idx := s.findLineIndex(code); idx >= 0 {
			s.lines[idx].Qty += qty
			s.recomputeTotals()
			return &s.basket, true
		}
	}
	s.recomputeTotals()
	return &s.basket, false
}

// AddLineWithModifiers adds a resolved base line to the basket with chosen
// customizations applied (ADR-0020) — used by the modifier-selection step,
// not the plain barcode-scan path. Price deltas are folded into PriceCents
// here so the rest of the money pipeline (totals, tax, receipts, held-sale
// snapshots) needs no changes; Modifiers is kept purely as a display/
// persistence snapshot. base must already be resolved (via PriceResolver or
// equivalent catalog lookup) with its plain, pre-modifier PriceCents.
func (s *Service) AddLineWithModifiers(base BasketLine, qty float64, mods []data.SelectedModifier) *Basket {
	if qty <= 0 {
		qty = 1
	}
	line := base
	line.Qty = qty
	line.Modifiers = append([]data.SelectedModifier{}, mods...)
	for _, m := range mods {
		line.PriceCents = line.PriceCents.Add(money.FromMinor(m.PriceDeltaMinor))
	}
	s.mergeResolved(line)
	return &s.basket
}

func (s *Service) HasLine(code string) bool {
	code = strings.TrimSpace(code)
	return code != "" && s.findLineIndex(code) >= 0
}

func (s *Service) HasScanCache(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	_, ok := s.scanCache[code]
	return ok
}

func (s *Service) cacheScan(code string, line BasketLine) {
	if code == "" {
		return
	}
	if s.scanCache == nil {
		s.scanCache = map[string]BasketLine{}
	}
	if len(s.scanCache) >= 1024 {
		// drop one entry to avoid unbounded growth in long sessions
		for k := range s.scanCache {
			delete(s.scanCache, k)
			break
		}
	}
	s.scanCache[code] = line
}

func (s *Service) mergeResolved(line BasketLine) {
	sig := line.ModifierSignature()
	for i := range s.lines {
		if s.lines[i].ModifierSignature() != sig {
			// Same item/SKU but a different customization (e.g. "extra
			// shot" vs. plain) prices and prints differently — must stay a
			// distinct line, not merge quantity into the wrong one.
			continue
		}
		if s.lines[i].SKU == line.SKU || (s.lines[i].ItemID == line.ItemID && s.lines[i].VariantID == line.VariantID) {
			s.lines[i].Qty += line.Qty
			s.lines[i].Name = line.Name
			s.lines[i].PriceCents = line.PriceCents
			s.lines[i].TaxRateBP = line.TaxRateBP
			s.lines[i].ItemID = line.ItemID
			s.lines[i].VariantID = line.VariantID
			s.lines[i].IsWeighed = line.IsWeighed
			s.lines[i].Modifiers = line.Modifiers
			if line.ImageURL != "" {
				s.lines[i].ImageURL = line.ImageURL
			}
			s.recomputeTotals()
			return
		}
	}
	if line.LineKey == "" {
		line.LineKey = uuid.NewString()
	}
	s.lines = append(s.lines, line)
	s.recomputeTotals()
}

func (s *Service) findLineIndex(code string) int {
	for i := range s.lines {
		if s.lines[i].SKU == code || s.lines[i].ItemID == code || s.lines[i].VariantID == code {
			return i
		}
	}
	return -1
}

func (s *Service) recomputeTotals() {
	var sub money.Money
	for i := range s.lines {
		l := &s.lines[i]
		lineBase := AmountForQuantity(l.PriceCents, l.Qty)
		lineNet := lineBase.Sub(l.LineDiscount)
		if lineNet.IsNegative() {
			lineNet = 0
		}
		l.LineTotal = lineNet
		sub = sub.Add(lineNet)
	}
	s.basket.Lines = append([]BasketLine{}, s.lines...)
	s.basket.Subtotal = sub
	var discount money.Money
	switch s.discountType {
	case "percent":
		if s.discountPercentBP > 0 && sub.IsPositive() {
			// round to nearest minor unit (unchanged: (sub*bp + 9999)/10000)
			discount = money.FromMinor((sub.Minor()*int64(s.discountPercentBP) + 9999) / 10000)
		}
	default:
		discount = s.discountValue
	}
	if discount.IsNegative() {
		discount = 0
	}
	s.basket.Discount = discount
	s.basket.DiscountType = s.discountType
	if s.discountType == "percent" {
		s.basket.DiscountRaw = s.discountPercentBP
	} else {
		s.basket.DiscountRaw = s.discountValue.Minor()
	}
	s.basket.CustomerID = s.customerID
	s.basket.CustomerName = s.customerName
	tax, total := money.Zero, sub
	if s.tax != nil {
		tax, total = s.tax.Compute(sub)
	}
	s.basket.Tax = tax
	total = total.Sub(discount)
	if total.IsNegative() {
		total = 0
	}
	s.basket.Total = total
}

func (s *Service) Tender(amount money.Money, method string) (map[string]any, error) {
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
//
// Matches every line sharing sku — was always safe before modifiers
// existed (mergeResolved guaranteed at most one line per SKU), but once
// AddLineWithModifiers can leave two lines with the same SKU in the basket
// (e.g. plain "Flat White" and "Flat White + extra shot"), this removes
// BOTH. The cashier UI uses RemoveLine (by LineKey) instead, which targets
// exactly one line; this SKU-based version is kept for callers that only
// ever have one line per SKU (nothing modifier-bearing).
func (s *Service) Remove(sku string) {
	filtered := s.lines[:0]
	for _, l := range s.lines {
		if l.SKU == sku {
			continue
		}
		filtered = append(filtered, l)
	}
	s.lines = filtered
	s.clearCacheForCode(sku)
	s.recomputeTotals()
}

// RemoveLine removes the single line matching key exactly (ADR-0020) —
// safe even when multiple lines share a SKU because they carry different
// modifiers, unlike Remove(sku) which would delete all of them.
func (s *Service) RemoveLine(key string) {
	filtered := s.lines[:0]
	for _, l := range s.lines {
		if l.LineKey == key {
			s.clearCacheForCode(l.SKU)
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
	s.scanCache = map[string]BasketLine{}
}

func (s *Service) clearCacheForCode(code string) {
	if code == "" || len(s.scanCache) == 0 {
		return
	}
	for key, line := range s.scanCache {
		if line.SKU == code || line.ItemID == code || line.VariantID == code {
			delete(s.scanCache, key)
		}
	}
}

// UpdateLine sets qty/discount for a given SKU (or item/variant match) and
// recomputes totals. Matches only the FIRST line for code — same caveat as
// Remove (see its doc comment): unsafe once multiple modifier-distinct
// lines can share a SKU. The cashier UI uses UpdateLineByKey instead.
func (s *Service) UpdateLine(code string, qty float64, discount money.Money) {
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

// UpdateLineByKey sets qty/discount for the single line matching key
// exactly (ADR-0020) — safe even when multiple lines share a SKU.
func (s *Service) UpdateLineByKey(key string, qty float64, discount money.Money) {
	if qty < 0 {
		qty = 0
	}
	for i := range s.lines {
		if s.lines[i].LineKey == key {
			s.lines[i].Qty = qty
			s.lines[i].LineDiscount = discount
			if s.lines[i].Qty == 0 {
				s.RemoveLine(key)
				return
			}
			s.recomputeTotals()
			return
		}
	}
}

// SetDiscount sets the sale-level discount (minor units) and recomputes totals.
func (s *Service) SetDiscount(discount money.Money) {
	if discount.IsNegative() {
		discount = 0
	}
	s.discountType = "amount"
	s.discountValue = discount
	s.discountPercentBP = 0
	s.recomputeTotals()
}

// SaleDiscount returns the current sale-level discount.
func (s *Service) SaleDiscount() money.Money {
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
