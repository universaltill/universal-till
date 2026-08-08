package pos

import (
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

type PriceResolver interface {
	Resolve(code string) (BasketLine, bool)
}

type Service struct {
	// mu guards all mutable state below (ut-docs#449): a Service instance is
	// shared by every request handler (one goroutine per request, net/http's
	// normal model), so exported methods must serialize access themselves.
	//
	// Locking pattern — mu is a plain, NON-reentrant Mutex, so every EXPORTED
	// method takes it exactly once at its own top and then only ever calls
	// unexported lock-free cores (scanQty, removeLocked, resetLocked, ...),
	// never another exported method on the same receiver. Unexported methods
	// assume mu is already held. Breaking this convention self-deadlocks
	// (hangs, not fails) the first time the reentrant path is exercised.
	mu sync.Mutex

	cfg       Config
	basket    Basket
	resolver  PriceResolver
	lines     []BasketLine // persisted after completion
	scanCache map[string]BasketLine
	// discount can be fixed amount or percentage basis points (1% = 100)
	discountType      string
	discountValue     money.Money // fixed discount amount (minor units)
	discountPercentBP int64       // percentage discount as basis points (a rate, not money)
	customerID        string
	customerName      string
	// orderType is "" (no explicit choice) or OrderTypeTakeaway. What (if
	// anything) it does to tax is entirely up to taxAsker.
	orderType string
	// taxAsker, when set, can override a line's tax rate per the current
	// order type — see TaxRateAsker. nil (the default) means core just uses
	// each line's own configured rate, unaffected by order type.
	taxAsker TaxRateAsker
}

type Config struct {
	TaxInclusive       bool
	TaxRateBasisPoints int // e.g. 2000 = 20.00%
	// ServiceChargeRateBasisPoints (ut-docs#72) is the till-set service
	// charge rate, applied to (subtotal - discount) and added on top --
	// live/display-only here (it drives what the basket shows BEFORE
	// tender); CompleteSale itself is given the already-computed amount,
	// not this rate, so a synced/replayed sale never recomputes against
	// whatever rate happens to be configured at replay time.
	ServiceChargeRateBasisPoints int
}

// OrderTypeTakeaway is a value merchants may use for OrderType — dine-in vs.
// takeaway is a genuinely common distinction (SumUp has the same concept),
// so core keeps the label as a shared convention. What (if anything) it does
// to a line's tax rate is entirely up to an installed TaxRateAsker: core has
// no built-in notion of any country's tax rules.
const OrderTypeTakeaway = "takeaway"

// TaxRateAsker lets an installed plugin override a line's effective tax
// rate — e.g. a country-specific order-type VAT switch (Germany's §12 UStG:
// some items tax differently for takeaway than dine-in). ok=false means the
// plugin has no opinion on this line; the line's own configured rate is
// used, same as when no asker is set at all. Wired from internal/pages via
// Service.SetTaxRateAsker, calling into the plugin event bus — internal/pos
// itself never talks to the plugin subsystem, keeping this package's only
// dependency on "what a country's tax rules are" as a pluggable interface.
type TaxRateAsker interface {
	AskTaxRateBP(l BasketLine, orderType string) (bp int, ok bool)
}

// effectiveTaxRateBP resolves the basis points to actually charge for one
// line under the current order type: ask the installed TaxRateAsker (if
// any) first, then fall back to the line's own configured rate — the same
// plain default as before any tax plugin existed.
func (s *Service) effectiveTaxRateBP(l BasketLine) int {
	if s.taxAsker != nil {
		if bp, ok := s.taxAsker.AskTaxRateBP(l, s.orderType); ok {
			return bp
		}
	}
	standard := l.TaxRateBP
	if standard == 0 {
		standard = s.cfg.TaxRateBasisPoints
	}
	if standard == 0 {
		standard = 2000 // default to 20% if unconfigured
	}
	return standard
}

func NewServiceWithResolver(cfg Config, r PriceResolver) *Service {
	return &Service{cfg: cfg, resolver: r, scanCache: map[string]BasketLine{}}
}

// SetTaxRateAsker installs (or clears, with nil) the plugin-backed tax-rate
// override hook and recomputes totals so the change is reflected immediately.
func (s *Service) SetTaxRateAsker(a TaxRateAsker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.taxAsker = a
	s.recomputeTotals()
}

// SetConfig swaps the tax configuration IN PLACE, keeping the basket.
// Background config refreshes (e.g. the LAN-sync pull) must use this
// rather than replacing the service — a replacement engine is empty and
// would silently discard a sale in progress.
func (s *Service) SetConfig(cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.recomputeTotals()
}

// Config returns the service's current tax configuration.
func (s *Service) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

type BasketLine struct {
	LineKey      string      `json:"lineKey,omitempty"` // stable per-line id, assigned when a line is first appended (ADR-0020) — the only safe way to address ONE line once modifiers let several share a SKU
	SKU          string      `json:"sku"`
	Name         string      `json:"name"`
	Qty          float64     `json:"qty"`
	PriceCents   money.Money `json:"priceCents"` // effective unit price: base + sum(Modifiers deltas)
	LineDiscount money.Money `json:"lineDiscount,omitempty"`
	LineTotal    money.Money `json:"lineTotal,omitempty"`
	ImageURL     string      `json:"imageUrl,omitempty"`
	ItemID       string      `json:"-"`
	VariantID    string      `json:"-"`
	TaxRateBP    int         `json:"-"`
	// TaxCodeID identifies which tax code this line's rate came from (empty
	// if the item has none and it's using the shop's global default rate) —
	// passed to TaxRateAsker so a tax plugin can distinguish item categories
	// without core needing to know what any of them mean.
	TaxCodeID string                  `json:"-"`
	IsWeighed bool                    `json:"-"`
	Modifiers []data.SelectedModifier `json:"modifiers,omitempty"` // ADR-0020: chosen customizations, already folded into PriceCents
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
	Lines    []BasketLine `json:"lines"`
	Subtotal money.Money  `json:"subtotal"`
	Tax      money.Money  `json:"tax"`
	Total    money.Money  `json:"total"`
	Discount money.Money  `json:"discount"`
	// ServiceCharge (ut-docs#72) is already folded into Total -- broken out
	// here, like Tax and Discount, so the sale screen can show it as its
	// own line rather than leaving the gap between Subtotal+Tax and Total
	// unexplained.
	ServiceCharge money.Money `json:"serviceCharge,omitempty"`
	DiscountType  string      `json:"discountType,omitempty"` // amount|percent
	DiscountRaw   int64       `json:"discountRaw,omitempty"`  // minor units or basis points (kept raw)
	CustomerID    string      `json:"customerId,omitempty"`
	CustomerName  string      `json:"customerName,omitempty"`
	ToastMessage  string      `json:"toastMessage,omitempty"`
	// ToastLevel classifies ToastMessage for the sale screen's single
	// notification surface (ut-docs#213): "info" (default), "success" or
	// "error". Errors persist until dismissed; info/success auto-expire.
	ToastLevel string `json:"toastLevel,omitempty"`
	// OrderType is "" (dine-in/standard) or OrderTypeTakeaway.
	OrderType string `json:"orderType,omitempty"`
}

// ItemCount is the total quantity in the basket for the sale screen's
// count badge: unit lines contribute their quantity, weighed lines
// (0.35 kg of cheese) read as one item each rather than a fraction.
func (b Basket) ItemCount() int {
	n := 0
	for _, l := range b.Lines {
		if l.IsWeighed {
			n++
			continue
		}
		n += int(l.Qty + 0.5)
	}
	return n
}

// basketCopyLocked returns a pointer to a private copy of the current basket,
// safe for the caller to read (and decorate with e.g. ToastMessage) after
// s.mu is released. Returning &s.basket directly would alias live state that
// the next locked call rewrites under the reader. The Lines slice inside the
// copy is safe to share: recomputeTotals always publishes a freshly allocated
// slice and nothing writes to its elements afterwards. Caller must hold s.mu.
func (s *Service) basketCopyLocked() *Basket {
	b := s.basket
	return &b
}

func (s *Service) Scan(code string) (*Basket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scanQty(code, 1)
	return s.basketCopyLocked(), nil
}

// ResolveBase resolves code to its plain, pre-modifier BasketLine via the
// configured PriceResolver WITHOUT adding it to the basket (ADR-0020) — the
// modifier-selection step needs the item's base price/name before the
// operator has chosen any customization, to build the picker and then call
// AddLineWithModifiers once they have.
//
// Deliberately NOT locked: it only reads s.resolver, which is set once at
// construction (NewServiceWithResolver) and never reassigned — verify that
// still holds before adding any resolver-swapping method.
func (s *Service) ResolveBase(code string) (BasketLine, bool) {
	code = strings.TrimSpace(code)
	if s.resolver == nil || code == "" {
		return BasketLine{}, false
	}
	return s.resolver.Resolve(code)
}

func (s *Service) ScanQty(code string, qty float64) (*Basket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scanQty(code, qty)
	return s.basketCopyLocked(), nil
}

func (s *Service) ScanQtyWithResult(code string, qty float64) (*Basket, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, found := s.scanQty(code, qty)
	return s.basketCopyLocked(), found, nil
}

// scanQty is the lock-free core shared by Scan/ScanQty/ScanQtyWithResult.
// Caller must hold s.mu.
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
	s.mu.Lock()
	defer s.mu.Unlock()
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
	return s.basketCopyLocked()
}

func (s *Service) HasLine(code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	code = strings.TrimSpace(code)
	return code != "" && s.findLineIndex(code) >= 0
}

func (s *Service) HasScanCache(code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.basket.OrderType = s.orderType
	// Per-line, not a single subtotal-wide rate: lines can carry different
	// tax codes, and the order-type dine-in/takeaway switch (§12 UStG) only
	// changes SOME lines' rate — a flat sub-wide engine can't represent that.
	var tax, total money.Money
	for i := range s.lines {
		l := &s.lines[i]
		rateBP := s.effectiveTaxRateBP(*l)
		lineTax, lineTotal := ComputeTaxBasisPoints(l.LineTotal, rateBP, s.cfg.TaxInclusive)
		tax = tax.Add(lineTax)
		total = total.Add(lineTotal)
	}
	s.basket.Tax = tax
	// Service charge (ut-docs#72): same base as CompleteSale/pos_api.go use
	// -- the pre-tax net subtotal, after discount -- so what's shown here,
	// before tender, matches what CompleteSale will actually demand.
	serviceCharge, _ := ComputeTaxBasisPoints(sub.Sub(discount), s.cfg.ServiceChargeRateBasisPoints, false)
	s.basket.ServiceCharge = serviceCharge
	total = total.Sub(discount).Add(serviceCharge)
	if total.IsNegative() {
		total = 0
	}
	s.basket.Total = total
}

func (s *Service) Tender(amount money.Money, method string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// reset basket for demo
	s.basket = Basket{}
	s.lines = nil
	s.orderType = ""
	return map[string]any{"status": "ok", "method": method, "amount": amount}, nil
}

// OrderType returns the current sale's order type ("" or OrderTypeTakeaway).
func (s *Service) OrderType() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orderType
}

// EffectiveLineTaxRateBP resolves the basis points to actually charge line l
// under the sale's current order type — the single source of truth callers
// (recomputeTotals' live preview, and the tender handler recording the final
// sale) must both use, so what a cashier sees pre-payment matches what gets
// recorded/receipted.
func (s *Service) EffectiveLineTaxRateBP(l BasketLine) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.effectiveTaxRateBP(l)
}

// SetOrderType sets the sale's order type and re-derives every current
// line's effective tax rate for it — including lines added before the
// order type was chosen or changed (a customer switching eat-in/takeaway
// mid-order, per docs/germany-pos-parity-backlog.md's dine-in/takeaway VAT
// section). Each line's own TaxRateBP/TakeawayRateBP are untouched (they
// stay the line's dine-in/takeaway pair); only recomputeTotals' downstream
// tax total reflects the switch.
func (s *Service) SetOrderType(orderType string) *Basket {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orderType = orderType
	s.recomputeTotals()
	return s.basketCopyLocked()
}

// Lines returns a copy of the current basket lines.
func (s *Service) Lines() []BasketLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]BasketLine{}, s.lines...)
}

// Basket returns the current basket snapshot.
func (s *Service) Basket() Basket {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(sku)
}

// removeLocked is Remove's lock-free core — also called by updateLineLocked
// when an update zeroes a quantity. Caller must hold s.mu.
func (s *Service) removeLocked(sku string) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLineLocked(key)
}

// removeLineLocked is RemoveLine's lock-free core — also called by
// updateLineByKeyLocked when an update zeroes a quantity. Caller must hold s.mu.
func (s *Service) removeLineLocked(key string) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetLocked()
}

// resetLocked is Reset's lock-free core — also called by Restore before
// loading a snapshot. Caller must hold s.mu.
func (s *Service) resetLocked() {
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
	s.orderType = ""
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if qty < 0 {
		qty = 0
	}
	for i := range s.lines {
		if s.lines[i].SKU == code || (s.lines[i].ItemID == code || s.lines[i].VariantID == code) {
			s.lines[i].Qty = qty
			s.lines[i].LineDiscount = discount
			if s.lines[i].Qty == 0 {
				s.removeLocked(s.lines[i].SKU)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if qty < 0 {
		qty = 0
	}
	for i := range s.lines {
		if s.lines[i].LineKey == key {
			s.lines[i].Qty = qty
			s.lines[i].LineDiscount = discount
			if s.lines[i].Qty == 0 {
				s.removeLineLocked(key)
				return
			}
			s.recomputeTotals()
			return
		}
	}
}

// SetDiscount sets the sale-level discount (minor units) and recomputes totals.
func (s *Service) SetDiscount(discount money.Money) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recomputeTotals()
	return s.basket.Discount
}

// SetDiscountPercent sets a sale-level percentage discount (basis points, 1% = 100).
func (s *Service) SetDiscountPercent(bp int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.customerID
}

// SetCustomerID attaches the basket to a customer (or clears when empty).
func (s *Service) SetCustomerID(customerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.customerID = customerID
	s.basket.CustomerID = customerID
}

// SetCustomer sets both id and name for the basket.
func (s *Service) SetCustomer(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setCustomerLocked(id, name)
}

// setCustomerLocked is SetCustomer's lock-free core — also called by Restore
// when loading a snapshot. Caller must hold s.mu.
func (s *Service) setCustomerLocked(id, name string) {
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
