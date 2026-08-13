package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// shopItem is one tile on the kiosk browse grid. Deliberately a distinct
// type from ui.Button/ButtonVM (the cashier's shortcut-button grid, a
// curated admin-configured subset) — the kiosk shows the FULL active
// catalog, a "menu" not a cashier's quick-tap shortcuts.
type shopItem struct {
	ItemID       string
	Name         string
	Description  string
	CategoryID   string
	Code         string // primary barcode, falling back to SKU — what /api/self-order/scan resolves against
	PriceMinor   int64
	HasModifiers bool
	ImageURL     string
}

// loadShopItems returns every active catalog item as a kiosk browse tile.
// The kiosk is category-browsing only (ut-docs#419) — there is no
// server-side name search here; category-chip filtering is client-side JS
// over this full set, keyed off each tile's data-cat attribute.
func loadShopItems(ctx context.Context, d *common.Deps) ([]shopItem, error) {
	repo := data.NewCatalogRepo(d.Db)
	items, err := repo.ListItems(ctx)
	if err != nil {
		return nil, err
	}
	barcodes, err := repo.ItemBarcodes(ctx)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	hasMods, _ := data.NewModifierRepo(d.Db).ItemIDsWithModifiers(ctx, ids)

	out := make([]shopItem, 0, len(items))
	for _, it := range items {
		code := it.SKU
		if bcs := barcodes[it.ID]; len(bcs) > 0 {
			code = bcs[0] // primary first, per CatalogRepo.ItemBarcodes ordering
		}
		if code == "" {
			continue // nothing to scan/resolve this item by — can't be added to a cart
		}
		categoryID := ""
		if it.CategoryID != nil {
			categoryID = *it.CategoryID
		}
		out = append(out, shopItem{
			ItemID:       it.ID,
			Name:         it.Name,
			Description:  it.Description,
			CategoryID:   categoryID,
			Code:         code,
			PriceMinor:   it.BasePrice,
			HasModifiers: hasMods[it.ID],
			ImageURL:     "/public/assets/items/" + it.ID + "/thumb.png",
		})
	}
	return out, nil
}

// registerSelfOrderShop wires the kiosk browse/search/cart flow (ADR-0020
// Phase 3). Everything here is under /self-order or /api/self-order,
// exempt from the auth middleware (anonymous customers) — see
// internal/auth/middleware.go. Deliberately NOT a thin re-registration of
// the cashier's /api/pos/* handlers: those carry cashier-only behavior
// (scan-to-refund on /api/pos/scan, a free-text discount field on
// /api/pos/line) that must never be reachable by an anonymous kiosk
// visitor. The security-critical modifier validation IS shared, via
// resolveAndValidateModifiers (pos_modifiers_api.go) — that logic has no
// cashier-only baggage and duplicating it would only risk the two copies
// drifting apart.
func registerSelfOrderShop(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /self-order/shop", func(w http.ResponseWriter, r *http.Request) {
		cats, _ := data.NewCatalogRepo(d.Db).ReadLookup(r.Context(), "categories")
		httpx.RenderPartial("ui/pages/self_order_shop.html", map[string]any{
			"title":         "Order here",
			"Categories":    cats,
			"idleResetSecs": d.CurrentState().KioskIdleResetSeconds,
		})(w, r)
	})

	// Cart-only render, for the page's own hx-trigger="load" fragment —
	// same pattern index.html uses for /ui/basket.
	mux.HandleFunc("GET /api/self-order/cart", func(w http.ResponseWriter, r *http.Request) {
		renderKioskCart(w, r, d)
	})

	// Renamed from /api/self-order/search (ut-docs#419) — the kiosk is
	// category-browsing only now, so this endpoint just loads the grid,
	// it doesn't search anything. Backs both the page's initial
	// hx-trigger="load" and re-renders after category-chip filtering
	// resets (chip filtering itself is client-side, see the page script).
	mux.HandleFunc("GET /api/self-order/grid", func(w http.ResponseWriter, r *http.Request) {
		items, err := loadShopItems(r.Context(), d)
		if err != nil {
			http.Error(w, "failed to load catalog", http.StatusInternalServerError)
			return
		}
		httpx.RenderPartial("ui/partials/self_order_grid.html", map[string]any{
			"Items": items,
		})(w, r)
	})

	mux.HandleFunc("POST /api/self-order/scan", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := strings.TrimSpace(r.Form.Get("code"))
		qty := 1.0
		if v := r.Form.Get("qty"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				qty = f
			}
		}
		if code != "" {
			// Item resolution + add ONLY — no promo-code-via-code fallback,
			// no scan-to-refund, no customer-barcode lookup. Those are
			// cashier-facing behaviors on /api/pos/scan that must not be
			// reachable from this anonymous surface.
			d.KioskEngine.ScanQtyWithResult(code, qty)
		}
		renderKioskCart(w, r, d)
	})

	mux.HandleFunc("GET /api/self-order/modifiers", func(w http.ResponseWriter, r *http.Request) {
		itemID := strings.TrimSpace(r.URL.Query().Get("item"))
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		base, ok := d.KioskEngine.ResolveBase(code)
		if !ok || itemID == "" {
			http.Error(w, "item not found", http.StatusNotFound)
			return
		}
		groups, err := data.NewModifierRepo(d.Db).ListGroupsForItem(r.Context(), itemID)
		if err != nil {
			http.Error(w, "failed to load customization options", http.StatusInternalServerError)
			return
		}
		httpx.RenderPartial("ui/partials/self_order_modifier_picker.html", map[string]any{
			"ItemID":   itemID,
			"Code":     code,
			"ItemName": base.Name,
			"Groups":   groups,
		})(w, r)
	})

	mux.HandleFunc("POST /api/self-order/scan-with-modifiers", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := strings.TrimSpace(r.Form.Get("code"))
		itemID := strings.TrimSpace(r.Form.Get("itemId"))

		base, selected, userMsg, err := resolveAndValidateModifiers(r.Context(), d, d.KioskEngine, code, itemID, r.Form)
		if err != nil {
			if userMsg == "" {
				http.Error(w, "failed to load customization options", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			renderKioskCartWithMessage(w, r, d, userMsg)
			return
		}
		qty := 1.0
		if v := r.Form.Get("qty"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				qty = f
			}
		}
		d.KioskEngine.AddLineWithModifiers(base, qty, selected)
		renderKioskCart(w, r, d)
	})

	// Qty-only line edit — deliberately no discount field at all (unlike
	// /api/pos/line's cashier-facing free-text discount, which would let
	// an anonymous customer manually cut their own bill). The +/- stepper
	// sends a relative delta (avoids needing template-side arithmetic to
	// compute an absolute value); an absolute qty is still accepted for
	// any future direct-entry UI.
	mux.HandleFunc("POST /api/self-order/line", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		key := strings.TrimSpace(r.Form.Get("key"))
		if key == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		qty := 0.0
		if v := strings.TrimSpace(r.Form.Get("delta")); v != "" {
			delta, err := strconv.ParseFloat(v, 64)
			if err != nil {
				http.Error(w, "invalid delta", http.StatusBadRequest)
				return
			}
			for _, l := range d.KioskEngine.Basket().Lines {
				if l.LineKey == key {
					qty = l.Qty + delta
					break
				}
			}
			if qty < 0 {
				qty = 0
			}
		} else if v := r.Form.Get("qty"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
				qty = f
			}
		}
		d.KioskEngine.UpdateLineByKey(key, qty, 0)
		renderKioskCart(w, r, d)
	})

	// Dine-in/takeaway toggle (ut-docs#260) — the kiosk-facing twin of the
	// cashier's /api/pos/order-type (pos_api.go). Same clamp: anything other
	// than the exact pos.OrderTypeTakeaway sentinel, including "", means
	// dine-in/standard, and this is also how a customer switches back.
	// Reuses Service.SetOrderType, so the same tax re-derivation
	// (EffectiveLineTaxRateBP) and the checkout handler's existing
	// SaleInput.OrderType wiring both pick this up with no further changes.
	mux.HandleFunc("POST /api/self-order/order-type", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		orderType := ""
		if r.Form.Get("order_type") == pos.OrderTypeTakeaway {
			orderType = pos.OrderTypeTakeaway
		}
		d.KioskEngine.SetOrderType(orderType)
		renderKioskCart(w, r, d)
	})

	mux.HandleFunc("POST /api/self-order/remove", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		key := strings.TrimSpace(r.Form.Get("key"))
		if key == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		d.KioskEngine.RemoveLine(key)
		renderKioskCart(w, r, d)
	})

	repo := data.NewPOSRepo(d.Db)

	// Payment-method picker (ADR-0020: card/contactless only, no cash
	// drawer at a kiosk) — shown in the same #selforder-modal the
	// customization picker uses.
	mux.HandleFunc("GET /api/self-order/checkout", func(w http.ResponseWriter, r *http.Request) {
		if len(d.KioskEngine.Lines()) == 0 {
			http.Error(w, "basket is empty", http.StatusBadRequest)
			return
		}
		methods, err := repo.ListActiveNonCashPaymentMethods(r.Context())
		if err != nil {
			http.Error(w, "failed to load payment methods", http.StatusInternalServerError)
			return
		}
		renderKioskPaymentPicker(w, r, d, methods, "")
	})

	mux.HandleFunc("POST /api/self-order/checkout", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		method := strings.TrimSpace(r.Form.Get("method"))

		lines := d.KioskEngine.Lines()
		if len(lines) == 0 {
			http.Error(w, "basket is empty", http.StatusBadRequest)
			return
		}

		methods, err := repo.ListActiveNonCashPaymentMethods(r.Context())
		if err != nil {
			http.Error(w, "failed to load payment methods", http.StatusInternalServerError)
			return
		}
		methodValid := false
		for _, m := range methods {
			if m.ID == method {
				methodValid = true
				break
			}
		}
		if !methodValid {
			// Never fall back to EnsurePaymentMethod here (as the cashier
			// tender handler does for its free-text quick-tender buttons):
			// that would silently create a new type='cash' payment_methods
			// row for any string a client sends. This surface is anonymous,
			// so the method MUST already be one of the shop's own active
			// non-cash methods.
			w.WriteHeader(http.StatusBadRequest)
			renderKioskPaymentPicker(w, r, d, methods, "selforder.checkout.invalid_method")
			return
		}

		locID, err := repo.EnsureStockLocation(r.Context())
		if err != nil {
			http.Error(w, "failed to prepare sale", http.StatusInternalServerError)
			return
		}
		saleLines, total, taxBlocked := kioskSaleLinesAndTotal(d, locID)
		// Fail closed (ut-docs#368): a line owned by a broken tax plugin
		// can't be priced trustworthily — refuse this checkout with a
		// customer-appropriate message (staff see the detail on the cashier
		// till and the plugins page) rather than charging a fallback rate.
		if taxBlocked {
			w.WriteHeader(http.StatusConflict)
			renderKioskPaymentPicker(w, r, d, methods, "selforder.checkout.tax_blocked")
			return
		}
		if !total.IsPositive() {
			w.WriteHeader(http.StatusBadRequest)
			renderKioskPaymentPicker(w, r, d, methods, "selforder.checkout.invalid_method")
			return
		}

		registerID, err := repo.EnsureRegister(r.Context())
		if err != nil {
			http.Error(w, "failed to prepare sale", http.StatusInternalServerError)
			return
		}

		allowNegative := d.CurrentState().AllowNegativeInventory
		if d.SyncPrimaryURL(r.Context()) != "" {
			allowNegative = true // a replica never gates on stock it doesn't own (ut-docs#404, ADR-0036) — same bypass as the cashier tender path
		}

		saleInput := pos.SaleInput{
			SaleType:     "sale",
			Currency:     d.CurrentState().Currency,
			TaxInclusive: d.CurrentState().TaxInclusive,
			Lines:        saleLines,
			Payments: []pos.PaymentInput{{
				MethodID: method,
				Amount:   total,
				Currency: d.CurrentState().Currency,
			}},
			RegisterID: registerID,
			// Kiosk sales are attributed to the seeded, PIN-less "kiosk"
			// operator (018_kiosk_user.sql) — never a value from the
			// anonymous request, unlike the cashier tender handler's
			// signed-in-operator CashierID.
			CashierID:              "kiosk",
			CustomerID:             d.KioskEngine.CustomerID(),
			OrderType:              d.KioskEngine.OrderType(),
			AllowNegativeInventory: allowNegative,
			ActorID:                "kiosk",
		}
		saleID, err := completeTender(r.Context(), d, d.KioskEngine, repo, saleInput, saleInput.Payments, "kiosk")
		if err != nil {
			var declined *paymentDeclinedError
			status := http.StatusBadRequest
			msgKey := "selforder.checkout.failed"
			if errors.As(err, &declined) {
				status = http.StatusPaymentRequired
				msgKey = "selforder.checkout.declined"
			}
			w.WriteHeader(status)
			renderKioskPaymentPicker(w, r, d, methods, msgKey)
			return
		}

		receiptNo, _, _, _, _ := repo.SaleTotals(r.Context(), saleID)
		if receiptNo == "" {
			receiptNo = saleID
		}
		httpx.RenderPartial("ui/partials/self_order_confirmation.html", map[string]any{
			"ReceiptNo": receiptNo,
		})(w, r)
	})
}

// kioskSaleLinesAndTotal converts the current basket into SaleLineInput rows
// and computes the payable total, mirroring the cashier tender handler's
// subtotal/tax math (/api/pos/tender in pos_api.go) exactly — a kiosk
// checkout must land on the same total a cashier would for an identical
// basket. Kiosk sales never carry a sale-level discount (no UI surfaces one
// to an anonymous customer), so this is deliberately simpler than the
// cashier path, which also honors a client- or basket-supplied discount.
// taxBlocked mirrors the cashier path's fail-closed check too (ut-docs#368):
// true when any line's tax is owned by a registered-but-broken tax plugin,
// in which case the checkout must be refused, not priced at a fallback rate.
func kioskSaleLinesAndTotal(d *common.Deps, locID string) ([]pos.SaleLineInput, money.Money, bool) {
	var saleLines []pos.SaleLineInput
	taxBlocked := false
	subtotal, taxTotal := money.Zero, money.Zero
	for _, l := range d.KioskEngine.Lines() {
		// Same resolution as the cashier tender handler (pos_api.go) —
		// required by this function's own invariant above.
		taxBP, lineTaxBlocked := d.KioskEngine.EffectiveLineTaxRateBP(l)
		taxBlocked = taxBlocked || lineTaxBlocked
		saleLines = append(saleLines, pos.SaleLineInput{
			ItemID:             l.ItemID,
			VariantID:          l.VariantID,
			SKU:                l.SKU,
			Barcode:            l.SKU,
			Name:               l.Name,
			Qty:                l.Qty,
			UnitPrice:          l.PriceCents,
			TaxRateBasisPoints: taxBP,
			LineDiscount:       l.LineDiscount,
			LocationID:         locID,
			Modifiers:          l.Modifiers,
		})
		lineBase := pos.AmountForQuantity(l.PriceCents, l.Qty)
		lineNet := lineBase.Sub(l.LineDiscount)
		lineTax, _ := pos.ComputeTaxBasisPoints(lineNet, taxBP, d.CurrentState().TaxInclusive)
		subtotal = subtotal.Add(lineNet)
		taxTotal = taxTotal.Add(lineTax)
	}
	total := subtotal
	if !d.CurrentState().TaxInclusive {
		total = total.Add(taxTotal)
	}
	if total.IsNegative() {
		total = money.Zero
	}
	return saleLines, total, taxBlocked
}

func renderKioskCart(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	renderKioskCartWithMessage(w, r, d, "")
}

func renderKioskCartWithMessage(w http.ResponseWriter, r *http.Request, d *common.Deps, message string) {
	b := d.KioskEngine.Basket()
	if message != "" {
		b.ToastMessage = message
	}
	httpx.RenderPartial("ui/partials/self_order_cart.html", map[string]any{
		"Basket": b,
	})(w, r)
}

// renderKioskPaymentPicker renders the payment-method modal. errKey, if set,
// is an i18n key (not raw text — this is a public, anonymous-facing surface)
// shown as an inline error above the method list.
func renderKioskPaymentPicker(w http.ResponseWriter, r *http.Request, d *common.Deps, methods []data.PaymentMethod, errKey string) {
	httpx.RenderPartial("ui/partials/self_order_payment_picker.html", map[string]any{
		"Methods": methods,
		"Total":   d.KioskEngine.Basket().Total,
		"ErrKey":  errKey,
	})(w, r)
}
