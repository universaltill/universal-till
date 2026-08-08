package pages

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
	uiassets "github.com/universaltill/universal-till/web"
)

// paymentDeclinedError signals that a plugin's blocking payment.<method>.authorize
// hook rejected the tender — distinct from other CompleteSale errors so callers
// can map it to its own HTTP status instead of a generic 400.
type paymentDeclinedError struct {
	Method string
}

func (e *paymentDeclinedError) Error() string {
	return "payment declined: " + e.Method
}

// completeTender runs the money-critical authorize -> complete -> publish
// pipeline shared by every till-mode's tender path (cashier and self-order
// kiosk alike): payment authorization gate, CompleteSale, basket reset,
// plugin trigger_event fan-out, ERP/inventory mirroring, and silent
// receipt/kitchen printing. It must behave identically regardless of the
// caller, so it lives in exactly one place rather than being duplicated
// per surface. actorID is attributed on printed receipts/tickets.
func completeTender(ctx context.Context, d *common.Deps, repo *data.POSRepo, saleInput pos.SaleInput, payments []pos.PaymentInput, actorID string) (string, error) {
	// Payment authorization (docs: wasm-runtime.md): a plugin method
	// whose plugin hooks `payment.<key>.authorize` gets a BLOCKING call
	// BEFORE the sale completes — a declined card must stop the sale.
	// blockingPaymentEvent (refund_page.go) is the same gate the refund
	// flow uses for payment.<key>.refund; no subscriber = no gate
	// (back-compat with post-settle-only plugins like qrpay).
	for i, p := range payments {
		resp, err := blockingPaymentEventWithResponse(ctx, d, p.MethodID, "authorize", map[string]any{
			"method":    p.MethodID,
			"amount":    p.Amount.Minor(),
			"reference": p.Reference,
		})
		if err != nil {
			return "", &paymentDeclinedError{Method: p.MethodID}
		}
		// A card-terminal plugin (e.g. a reader that prompts the customer
		// for a tip) can report the tip it actually captured back on its
		// authorize response — this overrides whatever tip (if any) the
		// tender request itself carried, since the reader-confirmed amount
		// is the source of truth. An absent/malformed/negative field is
		// ignored, leaving the request's own tip (typically zero) in place.
		if tip, ok := pluginReportedTipAmount(resp); ok {
			payments[i].TipAmount = money.FromMinor(tip)
		}
	}
	// Both call sites happen to pass a `payments` slice that shares
	// `saleInput.Payments`'s backing array, so the mutation above already
	// reaches CompleteSale by aliasing — but relying on that silently is
	// fragile (a caller passing a defensive copy would drop every
	// plugin-reported tip with no compile error and no test failure).
	// Make it explicit instead.
	saleInput.Payments = payments

	saleID, err := pos.CompleteSale(ctx, d.Db, saleInput)
	if err != nil {
		return "", err
	}

	d.Engine.Reset()

	// Plugin-provided tender methods: publish each entry's trigger_event so
	// the owning plugin can react (charge a terminal, show a QR, …).
	if entries, err := data.NewPluginRepo(d.Db).ListPaymentEntries(ctx); err == nil && len(entries) > 0 {
		byMethod := map[string]data.PaymentEntryRow{}
		for _, e := range entries {
			byMethod[e.EntryKey] = e
		}
		bus := plugins.SharedBus(d.Db)
		for _, p := range payments {
			if e, ok := byMethod[p.MethodID]; ok && e.TriggerEvent != "" {
				_, _ = bus.Publish(ctx, e.TriggerEvent, map[string]any{
					"sale_id":   saleID,
					"method":    p.MethodID,
					"amount":    p.Amount.Minor(),
					"reference": p.Reference,
					"plugin_id": e.PluginID,
				})
			}
		}
	}

	// Mirror the completed sale to external systems: publish sale.completed
	// on the plugin event bus so integration plugins (ERP/accounting
	// connectors — SAP, Dynamics/LS Central) can push it upstream. Best-
	// effort and NON-BLOCKING (offline-first): a slow or absent connector
	// never delays or fails the tender.
	publishSaleCompleted(ctx, d, saleID)

	// Mirror the resulting stock movements to external systems: one
	// stock.adjusted per line so inventory connectors can sync levels.
	// Best-effort and NON-BLOCKING, same as sale.completed.
	publishStockAdjustedForSale(ctx, d, saleInput)

	// load receipt_no from DB for printing (subtotal/tax/total re-read
	// separately by HTML-rendering callers, which need locale-aware funcs
	// this function doesn't have)
	receiptNo, _, _, _, _ := repo.SaleTotals(ctx, saleID)
	if receiptNo == "" {
		receiptNo = saleID
	}

	// Stock ownership is primary-only (ut-docs#404, ADR-0036): on the
	// primary — the till that owns stock — any resulting negative level
	// surfaces as a Problem, unconditionally: even with the gate ON, two
	// basket lines for the same item with different modifier signatures
	// don't merge (ADR-0020) and are each checked against the SAME
	// pre-sale figure, so a combination that individually passes the gate
	// per line can still land the item negative. On a replica the local
	// figure is only a cache — the primary re-derives the shop-wide level
	// from the arriving journal and surfaces any negative there instead —
	// so the completed sale nudges the journal-push loop for one
	// immediate attempt so the primary hears about it in seconds, not at
	// the next 30s tick. Non-blocking, best-effort: the tender never waits
	// on either (ADR-0003).
	if d.SyncPrimaryURL(ctx) == "" {
		warnIfStockNegative(ctx, repo, saleInput, "sale "+receiptNo)
	} else {
		d.RequestSyncPush()
	}

	// Silent receipt print (docs: receipt-printing.md) — fired async,
	// never blocks or fails the tender.
	printReceiptAsync(d, receiptNo, actorID)
	// Kitchen ticket to the separate kitchen printer, if one is
	// configured (docs: arch/restaurant-phone-orders.md) — also async
	// and best-effort; a no-op when no kitchen printer is set.
	printKitchenAsync(d, receiptNo, actorID)

	return saleID, nil
}

// pluginReportedTipAmount extracts an optional `tip_amount` (integer minor
// units, same convention as pos_api's own `tip` tender field) from a
// payment.<key>.authorize plugin's response — e.g. a card-terminal plugin
// that read back a customer-selected tip from the reader. ok is false (tip
// left as the request sent it) when resp is empty, unparseable, missing the
// field, or the field is negative — a plugin bug here must never invent or
// corrupt a tip, only ever report a real non-negative one.
func pluginReportedTipAmount(resp json.RawMessage) (amount int64, ok bool) {
	if len(resp) == 0 {
		return 0, false
	}
	var parsed struct {
		TipAmount *int64 `json:"tip_amount"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil || parsed.TipAmount == nil || *parsed.TipAmount < 0 {
		return 0, false
	}
	return *parsed.TipAmount, true
}

func registerPOSAPI(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewPOSRepo(d.Db)
	mux.HandleFunc("/api/pos/scan", func(w http.ResponseWriter, r *http.Request) {
		in := struct {
			Code       string  `json:"code"`
			Qty        float64 `json:"qty"`
			CustomerID string  `json:"customerId,omitempty"`
		}{Qty: 1}

		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			_ = json.NewDecoder(r.Body).Decode(&in)
		} else {
			_ = r.ParseForm()
			in.Code = r.Form.Get("code")
			in.CustomerID = r.Form.Get("customerId")
			if q := r.Form.Get("qty"); q != "" {
				if v, err := strconv.ParseFloat(q, 64); err == nil && v > 0 {
					in.Qty = v
				}
			}
		}
		code := strings.TrimSpace(in.Code)
		if in.Qty > 0 {
			if rQty, err := strconv.ParseFloat(fmt.Sprintf("%v", in.Qty), 64); err == nil {
				in.Qty = rQty
			}
		} else {
			in.Qty = 1
		}

		if cid := strings.TrimSpace(in.CustomerID); cid != "" {
			d.Engine.SetCustomerID(cid)
		}

		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		basketView, _ := ui.NewBasketView(funcs)
		render := func(b *pos.Basket) {
			_ = basketView.Render(w, b)
		}

		if code == "" {
			b := d.Engine.Basket()
			b.ToastMessage = httpx.T(locale, "pos.toast.scan_prompt")
			b.ToastLevel = "info"
			render(&b)
			return
		}

		if d.Engine.HasScanCache(code) || d.Engine.HasLine(code) {
			b, _, _ := d.Engine.ScanQtyWithResult(code, in.Qty)
			render(b)
			return
		}

		// If the scan is a customer barcode, attach and return current basket.
		if looksLikeCustomerCode(code) {
			if custID, custName, ok := repo.LookupCustomer(r.Context(), code); ok {
				d.Engine.SetCustomer(custID, custName)
				b := d.Engine.Basket()
				b.ToastMessage = fmt.Sprintf(httpx.T(locale, "pos.toast.customer_linked"), custName)
				b.ToastLevel = "success"
				render(&b)
				return
			}
			b := d.Engine.Basket()
			b.ToastMessage = httpx.T(locale, "pos.toast.customer_not_found")
			b.ToastLevel = "error"
			render(&b)
			return
		}

		// Fast path: resolve item before any DB lookups to keep scan latency low.
		if b, found, _ := d.Engine.ScanQtyWithResult(code, in.Qty); found {
			render(b)
			return
		}

		// Promo/discount codes checked only if item resolution fails.
		customerID := d.Engine.CustomerID()
		if promoType, value, ok := repo.FindActivePromo(r.Context(), customerID, code); ok {
			if promoType == "percent" {
				d.Engine.SetDiscountPercent(value)
			} else {
				d.Engine.SetDiscount(money.FromMinor(value))
			}
			b := d.Engine.Basket()
			b.ToastMessage = fmt.Sprintf(httpx.T(locale, "pos.toast.promo_applied"), code)
			b.ToastLevel = "success"
			render(&b)
			return
		}

		// Scan-to-refund (docs: refunds.md): a printed receipt carries its
		// number as a barcode — scanning it opens the refund screen.
		if exists, _ := repo.ReceiptExists(r.Context(), code); exists {
			w.Header().Set("HX-Redirect", "/refund/"+url.PathEscape(code))
			w.WriteHeader(http.StatusOK)
			return
		}

		b := d.Engine.Basket()
		b.ToastMessage = httpx.T(locale, "pos.toast.item_not_found")
		b.ToastLevel = "error"
		render(&b)
	})

	// Remove a line. Prefers the line-specific key (ADR-0020 — safe once an
	// item can have multiple modifier-distinct lines sharing one SKU);
	// falls back to the legacy SKU/code param for any caller that predates
	// LineKey.
	mux.HandleFunc("/api/pos/remove", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		key := strings.TrimSpace(r.Form.Get("key"))
		code := strings.TrimSpace(r.Form.Get("code"))
		if key == "" && code == "" {
			http.Error(w, "key or code required", http.StatusBadRequest)
			return
		}
		if key != "" {
			d.Engine.RemoveLine(key)
		} else {
			d.Engine.Remove(code)
		}
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		b := d.Engine.Basket()
		_ = basketView.Render(w, b)
	})

	// Update line qty/discount (htmx-friendly). Same key/code preference as
	// /api/pos/remove above.
	mux.HandleFunc("/api/pos/line", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		key := strings.TrimSpace(r.Form.Get("key"))
		code := strings.TrimSpace(r.Form.Get("code"))
		if key == "" && code == "" {
			http.Error(w, "key or code required", http.StatusBadRequest)
			return
		}
		qty := 0.0
		if v := r.Form.Get("qty"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
				qty = f
			}
		}
		discount := int64(0)
		if v := r.Form.Get("discount"); v != "" {
			if dVal, err := strconv.ParseInt(v, 10, 64); err == nil && dVal >= 0 {
				discount = dVal
			}
		}
		if key != "" {
			d.Engine.UpdateLineByKey(key, qty, money.FromMinor(discount))
		} else {
			d.Engine.UpdateLine(code, qty, money.FromMinor(discount))
		}
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		b := d.Engine.Basket()
		_ = basketView.Render(w, b)
	})

	// Apply sale-level discount (coupon/promotion) in minor units.
	mux.HandleFunc("/api/pos/discount", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		discount := int64(0)
		if v := r.Form.Get("discount"); v != "" {
			if dVal, err := strconv.ParseInt(v, 10, 64); err == nil && dVal >= 0 {
				discount = dVal
			}
		}
		d.Engine.SetDiscount(money.FromMinor(discount))
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		b := d.Engine.Basket()
		_ = basketView.Render(w, b)
	})

	// Dine-in/takeaway toggle (§12 UStG: some lines' VAT rate depends on
	// this — docs/germany-pos-parity-backlog.md). Any value other than
	// pos.OrderTypeTakeaway is treated as dine-in/standard, including "" —
	// this endpoint is also how a cashier switches BACK to dine-in.
	mux.HandleFunc("/api/pos/order-type", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		orderType := ""
		if r.Form.Get("order_type") == pos.OrderTypeTakeaway {
			orderType = pos.OrderTypeTakeaway
		}
		b := d.Engine.SetOrderType(orderType)
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		_ = basketView.Render(w, *b)
	})

	// Reset basket for new customer.
	mux.HandleFunc("/api/pos/reset", func(w http.ResponseWriter, r *http.Request) {
		d.Engine.Reset()
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		b, _ := d.Engine.Scan("")
		_ = basketView.Render(w, b)
	})

	mux.HandleFunc("/api/pos/tender", func(w http.ResponseWriter, r *http.Request) {
		type In struct {
			Payments []struct {
				Amount    int64  `json:"amount"`
				Method    string `json:"method"`
				Currency  string `json:"currency,omitempty"`
				Reference string `json:"reference,omitempty"`
				Change    int64  `json:"change,omitempty"`
				// Tip is gratuity captured alongside a card-terminal tender
				// (docs/germany-pos-parity-backlog.md tip-flow gap) -- set by
				// the caller (e.g. a SumUp reader plugin reading the tip back
				// from its Cloud API transaction result), same shape as the
				// existing cash `change` field. Never affects payment coverage.
				Tip int64 `json:"tip,omitempty"`
			} `json:"payments"`
			Discount      int64  `json:"discount,omitempty"`
			RegisterID    string `json:"registerId,omitempty"`
			CashierID     string `json:"cashierId,omitempty"`
			CustomerID    string `json:"customerId,omitempty"`
			AllowNegative *bool  `json:"allowNegativeInventory,omitempty"`
			Note          string `json:"note,omitempty"`
			SimFail       bool   `json:"simulateFailure,omitempty"`
			FailReason    string `json:"failureReason,omitempty"`
			Offline       *bool  `json:"offline,omitempty"`
		}
		var in In
		// Only JSON-decode a JSON body: decoding unconditionally consumed the
		// body, so the later ParseForm calls saw nothing and every quick-tender
		// button silently recorded "cash" whatever method was tapped.
		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
		} else {
			_ = r.ParseForm()
		}

		offline := false
		offlineSet := false
		if in.Offline != nil {
			offline = *in.Offline
			offlineSet = true
		}
		if !offlineSet {
			if err := r.ParseForm(); err == nil {
				switch strings.ToLower(strings.TrimSpace(r.Form.Get("offline_override"))) {
				case "1", "true", "yes", "on":
					offline = true
					offlineSet = true
				}
				if !offlineSet {
					switch strings.ToLower(strings.TrimSpace(r.Form.Get("offline"))) {
					case "1", "true", "yes", "on":
						offline = true
						offlineSet = true
					}
				}
			}
		}

		lines := d.Engine.Lines()
		if len(lines) == 0 {
			http.Error(w, "no items in basket", http.StatusBadRequest)
			return
		}

		locID, err := repo.EnsureStockLocation(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var saleLines []pos.SaleLineInput
		for _, l := range lines {
			// Single source of truth with the live basket preview
			// (Service.recomputeTotals) — same order-type-aware resolution,
			// so what the cashier saw pre-payment is what gets recorded.
			taxBP := d.Engine.EffectiveLineTaxRateBP(l)
			// Qty is int; convert to float64 for REAL support
			saleLines = append(saleLines, pos.SaleLineInput{
				ItemID:             l.ItemID,
				VariantID:          l.VariantID,
				SKU:                l.SKU,
				Barcode:            l.SKU,
				Name:               l.Name,
				Qty:                float64(l.Qty),
				UnitPrice:          l.PriceCents,
				TaxRateBasisPoints: taxBP,
				LineDiscount:       l.LineDiscount,
				LocationID:         locID,
				Modifiers:          l.Modifiers,
			})
		}

		var payments []pos.PaymentInput
		for _, p := range in.Payments {
			if p.Method == "" || p.Amount <= 0 {
				continue
			}
			if err := repo.EnsurePaymentMethod(r.Context(), p.Method); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			payments = append(payments, pos.PaymentInput{
				MethodID:    p.Method,
				Amount:      money.FromMinor(p.Amount),
				Currency:    p.Currency,
				Reference:   p.Reference,
				ChangeGiven: money.FromMinor(p.Change),
				TipAmount:   money.FromMinor(p.Tip),
			})
		}
		// Fallback for form-encoded tender buttons (hx-vals)
		if len(payments) == 0 {
			if err := r.ParseForm(); err == nil {
				method := r.Form.Get("method")
				amountStr := r.Form.Get("amount")
				var amount int64
				if amt, err := strconv.ParseInt(amountStr, 10, 64); err == nil && amt > 0 {
					amount = amt
				}
				if method != "" {
					if err := repo.EnsurePaymentMethod(r.Context(), method); err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					payments = append(payments, pos.PaymentInput{
						MethodID: method,
						Amount:   money.FromMinor(amount),
						Currency: d.CurrentState().Currency,
					})
				}
			}
		}
		// compute totals for receipt and fallback payment
		subtotal, taxTotal := money.Zero, money.Zero
		for i := range saleLines {
			lineBase := pos.AmountForQuantity(saleLines[i].UnitPrice, saleLines[i].Qty)
			lineNet := lineBase.Sub(saleLines[i].LineDiscount)
			lineTax, _ := pos.ComputeTaxBasisPoints(lineNet, saleLines[i].TaxRateBasisPoints, d.CurrentState().TaxInclusive)
			subtotal = subtotal.Add(lineNet)
			taxTotal = taxTotal.Add(lineTax)
		}
		discount := money.FromMinor(in.Discount)
		if discount.IsZero() {
			discount = d.Engine.SaleDiscount()
		}
		// Service charge (ut-docs#72) computed here, off the same
		// post-discount subtotal the basket engine uses for its own
		// on-screen total (internal/pos/service.go recomputeTotals) --
		// same rate, same base, so what's quoted on screen, what's
		// demanded here for a zero-amount tender button, and what
		// CompleteSale enforces all agree. The AMOUNT (not the rate) is
		// what flows into SaleInput, same shape as SaleDiscount.
		serviceCharge, _ := pos.ComputeTaxBasisPoints(subtotal.Sub(discount), d.CurrentState().ServiceChargeRateBasisPoints, false)
		total := subtotal.Sub(discount).Add(serviceCharge)
		if !d.CurrentState().TaxInclusive {
			total = total.Add(taxTotal)
		}
		if total.IsNegative() {
			total = 0
		}
		if len(payments) == 0 {
			payments = append(payments, pos.PaymentInput{
				MethodID: "cash",
				Amount:   total,
				Currency: d.CurrentState().Currency,
			})
		}
		for i := range payments {
			if !payments[i].Amount.IsPositive() {
				payments[i].Amount = total
			}
		}

		registerID := in.RegisterID
		if registerID == "" {
			if regID, err := repo.EnsureRegister(r.Context()); err == nil {
				registerID = regID
			}
		}

		allowNegative := d.CurrentState().AllowNegativeInventory
		if in.AllowNegative != nil {
			allowNegative = *in.AllowNegative
		}
		if d.SyncPrimaryURL(r.Context()) != "" {
			allowNegative = true // a replica never gates on stock it doesn't own (ut-docs#404, ADR-0036)
		}

		cashierID := in.CashierID
		if cashierID == "" {
			// Sales are attributed to the signed-in operator; the repo
			// fallback only remains for UT_AUTH=off tooling runs.
			if u, ok := auth.FromContext(r.Context()); ok {
				cashierID = u.ID
			} else if cid, err := repo.EnsureUser(r.Context()); err == nil {
				cashierID = cid
			}
		}

		customerID := in.CustomerID
		if customerID == "" {
			customerID = d.Engine.CustomerID()
		}

		if in.SimFail {
			failureReason := in.FailReason
			if failureReason == "" {
				failureReason = "simulated payment failure"
			}
			if _, err := pos.RecordPaymentFailure(r.Context(), d.Db, pos.PaymentFailure{
				ActorID:  cashierID,
				Reason:   failureReason,
				Payments: payments,
				Lines:    saleLines,
				Total:    total.Minor(),
				Currency: d.CurrentState().Currency,
			}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Error(w, "payment failed; retry required", http.StatusBadGateway)
			return
		}

		saleInput := pos.SaleInput{
			SaleType:               "sale",
			Currency:               d.CurrentState().Currency,
			TaxInclusive:           d.CurrentState().TaxInclusive,
			SaleDiscount:           discount,
			ServiceCharge:          serviceCharge,
			OrderType:              d.Engine.OrderType(),
			Lines:                  saleLines,
			Payments:               payments,
			Note:                   in.Note,
			RegisterID:             registerID,
			CashierID:              cashierID,
			CustomerID:             customerID,
			AllowNegativeInventory: allowNegative,
			ActorID:                cashierID,
			Offline:                offline,
		}
		saleID, err := completeTender(r.Context(), d, repo, saleInput, payments, getSessionUserID(r))
		if err != nil {
			var declined *paymentDeclinedError
			if errors.As(err, &declined) {
				http.Error(w, err.Error(), http.StatusPaymentRequired)
				return
			}
			if strings.Contains(err.Error(), "insufficient stock") {
				locale := httpx.ResolveLocale(w, r)
				funcs := httpx.FuncsFor(locale)
				b := d.Engine.Basket()
				// Localized, generic message on the persistent notice — the raw
				// engine error carries internal item/location IDs and English
				// prose that must not sit on a cashier-facing surface. The
				// detail stays available server-side via the log below.
				log.Printf("tender rejected: %v", err)
				b.ToastMessage = httpx.T(locale, "pos.toast.insufficient_stock")
				b.ToastLevel = "error"
				basketView, _ := ui.NewBasketView(funcs)
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				_ = basketView.Render(w, b)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// load receipt_no and totals from DB for rendering
		receiptNo, dbSubtotal, dbTax, dbTotal, _ := repo.SaleTotals(r.Context(), saleID)
		if receiptNo == "" {
			receiptNo = saleID
		}

		// Render receipt JSON if requested. Wrapped as { "data": …, "error":
		// null } -- the envelope universal-till/CLAUDE.md mandates for every
		// JSON API response (ut-docs#387: this endpoint used to respond bare).
		if r.Header.Get("Accept") == "application/json" {
			resp := map[string]any{
				"saleId":    saleID,
				"receiptNo": receiptNo,
				"total":     dbTotal,
				"payments":  payments,
				"note":      in.Note,
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": resp, "error": nil})
			return
		}

		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		basket := d.Engine.Basket()
		discountType := basket.DiscountType
		discountRaw := basket.DiscountRaw
		if in.Discount > 0 {
			discountType = "amount"
			discountRaw = in.Discount
		}

		completedAt, _, _ := repo.SaleCompletedAt(r.Context(), saleID)
		legalBlocks, err := loadReceiptLegalBlocks(r.Context(), d.Db, completedAt)
		if err != nil {
			legalBlocks = nil
		}
		printerAvailable, err := data.NewPluginRepo(d.Db).HasActivePrinterCapability(r.Context())
		if err != nil {
			printerAvailable = false
		}
		// The built-in ESC/POS path counts as a printer too.
		if printerConfig(r.Context(), d).Enabled() {
			printerAvailable = true
		}
		printerUnavailable := !printerAvailable
		receiptHTML, renderErr := renderReceipt(funcs, receiptNo, saleLines, payments, dbSubtotal, dbTax, dbTotal, d.CurrentState().TaxInclusive, discount.Minor(), discountType, discountRaw, legalBlocks, printerUnavailable,
			storeNameOrDefault(r.Context(), d), receiptDesignFromSettings(r.Context(), d))
		if renderErr != nil {
			printerUnavailable = true
			receiptHTML = `<div class="receipt-printer-warning"><span class="receipt-printer-message">` + template.HTMLEscapeString(funcs["T"].(func(string) string)("receipt.printer.unavailable")) + `</span><button class="btn secondary receipt-printer-retry" type="button" onclick="window.print()">` + template.HTMLEscapeString(funcs["T"].(func(string) string)("receipt.printer.retry")) + `</button></div>`
		}

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		journalView, jErr := ui.NewJournalView(funcs)
		if jErr == nil {
			entries, err := repo.ListRecentSales(r.Context(), 5)
			if err == nil {
				var journalBuf bytes.Buffer
				_ = journalView.Render(&journalBuf, ui.JournalViewData{Entries: entries, OOB: true})
				fmt.Fprintf(w, `<div class="basket receipt-view" id="basket">%s</div>%s`, receiptHTML, journalBuf.String())
				return
			}
		}
		fmt.Fprintf(w, `<div class="basket receipt-view" id="basket">%s</div>`, receiptHTML)
	})

	// Update sale status: park, void, refund (status string expected).
	mux.HandleFunc("/api/pos/sale/status", func(w http.ResponseWriter, r *http.Request) {
		type In struct {
			SaleID string `json:"saleId"`
			Status string `json:"status"` // open|parked|voided|refunded
			Reason string `json:"reason,omitempty"`
		}
		var in In
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		if in.SaleID == "" || in.Status == "" {
			http.Error(w, "saleId and status required", http.StatusBadRequest)
			return
		}
		if err := pos.UpdateSaleStatus(r.Context(), d.Db, in.SaleID, in.Status, getSessionUserID(r), in.Reason); err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, data.ErrSaleNotFound) {
				code = http.StatusNotFound
			}
			http.Error(w, err.Error(), code)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

type receiptLine struct {
	Name          string
	SKU           string // only set when the receipt design shows SKUs
	Qty           int
	TotalAfterTax int64
}

type receiptPayment struct {
	Method    string
	Applied   int64
	Change    int64
	Tip       int64
	Reference string
}

type receiptLegalBlock struct {
	PluginID      string
	PluginName    string
	PluginVersion string
	Priority      int
	Lines         []string
}

type receiptTemplateConfig struct {
	LegalText  string   `json:"legal_text"`
	LegalLines []string `json:"legal_lines"`
	Priority   int      `json:"priority"`
}

func loadReceiptLegalBlocks(ctx context.Context, db *sql.DB, completedAt time.Time) ([]receiptLegalBlock, error) {
	entries, err := data.NewPluginRepo(db).ListReceiptTemplates(ctx)
	if err != nil {
		return nil, err
	}
	var repo = data.NewPluginRepo(db)
	var blocks []receiptLegalBlock
	for _, entry := range entries {
		var cfg receiptTemplateConfig
		if entry.ConfigJSON != "" {
			if err := json.Unmarshal([]byte(entry.ConfigJSON), &cfg); err != nil {
				continue
			}
		}
		lines := normalizeLegalLines(cfg.LegalText, cfg.LegalLines)
		if len(lines) == 0 {
			continue
		}
		priority := cfg.Priority
		if priority == 0 {
			priority = entry.SortOrder
		}
		version := entry.PluginVersion
		if !completedAt.IsZero() {
			if v, ok, _ := repo.GetPluginVersionAt(ctx, entry.PluginID, completedAt); ok && v != "" {
				version = v
			}
		}
		blocks = append(blocks, receiptLegalBlock{
			PluginID:      entry.PluginID,
			PluginName:    entry.PluginName,
			PluginVersion: version,
			Priority:      priority,
			Lines:         lines,
		})
	}
	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].Priority != blocks[j].Priority {
			return blocks[i].Priority < blocks[j].Priority
		}
		if blocks[i].PluginID != blocks[j].PluginID {
			return blocks[i].PluginID < blocks[j].PluginID
		}
		return blocks[i].PluginVersion < blocks[j].PluginVersion
	})
	return blocks, nil
}

func normalizeLegalLines(text string, lines []string) []string {
	var out []string
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if strings.TrimSpace(text) == "" {
		return out
	}
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func renderReceipt(funcs template.FuncMap, receiptNo string, lines []pos.SaleLineInput, payments []pos.PaymentInput, subtotal, taxTotal, total int64, taxInclusive bool, saleDiscount int64, saleDiscountType string, saleDiscountRaw int64, legalBlocks []receiptLegalBlock, printerUnavailable bool, storeName string, design receiptDesign) (string, error) {
	t, err := template.New("receipt.html").Funcs(funcs).ParseFS(uiassets.FS,
		"ui/partials/receipt.html",
	)
	if err != nil {
		return "", err
	}
	var rlines []receiptLine
	for _, l := range lines {
		lineBase := pos.AmountForQuantity(l.UnitPrice, l.Qty)
		lineNet := lineBase.Sub(l.LineDiscount)
		lineTax, _ := pos.ComputeTaxBasisPoints(lineNet, l.TaxRateBasisPoints, taxInclusive)
		lineTotal := lineNet
		if !taxInclusive {
			lineTotal = lineTotal.Add(lineTax)
		}
		rl := receiptLine{
			Name:          l.Name,
			Qty:           int(l.Qty),
			TotalAfterTax: lineTotal.Minor(),
		}
		if design.ShowSKU {
			rl.SKU = l.SKU
		}
		rlines = append(rlines, rl)
	}
	var paymentViews []receiptPayment
	for _, p := range payments {
		applied := p.Amount.Sub(p.ChangeGiven)
		if applied.IsNegative() {
			applied = 0
		}
		paymentViews = append(paymentViews, receiptPayment{
			Method:    p.MethodID,
			Applied:   applied.Minor(),
			Change:    p.ChangeGiven.Minor(),
			Tip:       p.TipAmount.Minor(),
			Reference: p.Reference,
		})
	}
	data := map[string]any{
		"ReceiptNo":          receiptNo,
		"Lines":              rlines,
		"Payments":           paymentViews,
		"Subtotal":           subtotal,
		"TaxTotal":           taxTotal,
		"SaleDiscount":       saleDiscount,
		"SaleDiscountType":   saleDiscountType,
		"SaleDiscountRaw":    saleDiscountRaw,
		"Total":              total,
		"LegalBlocks":        legalBlocks,
		"PrinterUnavailable": printerUnavailable,
		// Receipt design (docs: receipt-designer.md): the on-screen copy
		// follows the same owner-styled design as the thermal print.
		"StoreName":    storeName,
		"DesignHeader": design.Header,
		"DesignFooter": design.Footer,
		"ShowTax":      design.ShowTax,
		"ShowBarcode":  design.ShowBarcode,
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "receipt", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func looksLikeCustomerCode(code string) bool {
	c := strings.ToUpper(strings.TrimSpace(code))
	if c == "" {
		return false
	}
	if strings.HasPrefix(c, "CUST") || strings.HasPrefix(c, "LOY-") {
		return true
	}
	if strings.HasPrefix(c, "LOY") && len(c) > 3 {
		next := c[3]
		return next >= '0' && next <= '9'
	}
	return false
}

// publishSaleCompleted loads the authoritative sale snapshot and publishes a
// "sale.completed" event for integration plugins (ERP/accounting connectors).
// Best-effort: any error is swallowed so it never affects the tender path
// (offline-first — dispatch is non-blocking on the event bus).
func publishSaleCompleted(ctx context.Context, d *common.Deps, saleID string) {
	detail, ok, err := data.NewPOSRepo(d.Db).GetSaleDetailByID(ctx, saleID)
	if err != nil || !ok {
		return
	}
	ev := plugins.SaleCompletedEvent{
		SaleID:        detail.ID,
		ReceiptNo:     detail.ReceiptNo,
		SaleType:      detail.SaleType,
		Currency:      detail.Currency,
		SubtotalCents: detail.Subtotal,
		DiscountCents: detail.DiscountTotal,
		TaxCents:      detail.TaxTotal,
		TotalCents:    detail.Total,
		CashierID:     detail.CashierID,
		CompletedAt:   time.Now().UTC(),
	}
	for _, l := range detail.Lines {
		ev.LineItems = append(ev.LineItems, plugins.SaleLineItem{
			ItemID:         l.ItemID,
			VariantID:      l.VariantID,
			SKU:            l.SKU,
			Name:           l.Name,
			Quantity:       l.Qty,
			UnitPriceCents: l.UnitPrice,
			DiscountCents:  l.LineDiscount,
			TaxRateBP:      l.TaxRateBP,
			TaxCents:       l.TaxAmount,
			TotalCents:     l.LineTotal,
		})
	}
	for i, p := range detail.Payments {
		if i == 0 {
			ev.PaymentMethod = p.Method
		}
		ev.Payments = append(ev.Payments, plugins.SalePayment{
			Method:      p.Method,
			AmountCents: p.Amount,
			Reference:   p.Reference,
		})
	}
	_, _ = plugins.SharedBus(d.Db).PublishSaleCompleted(ctx, ev)
}

// publishStockAdjusted publishes a single "stock.adjusted" event for
// integration plugins (ERP/inventory connectors — ADR-0014). Best-effort and
// NON-BLOCKING: any error is swallowed and dispatch is non-blocking on the bus,
// so it never delays or fails the underlying sale/refund/adjustment
// (offline-first). A zero delta is ignored.
func publishStockAdjusted(ctx context.Context, d *common.Deps, ev plugins.StockAdjustedEvent) {
	if ev.DeltaQty == 0 {
		return
	}
	if ev.AdjustedAt.IsZero() {
		ev.AdjustedAt = time.Now().UTC()
	}
	_, _ = plugins.SharedBus(d.Db).PublishStockAdjusted(ctx, ev)
}

// publishStockAdjustedForSale emits one stock.adjusted per line for a completed
// sale or return, mirroring the signed stock movement CompleteSale wrote (a
// sale takes stock out → negative delta; a return puts it back → positive).
// Best-effort/non-blocking, per publishStockAdjusted.
func publishStockAdjustedForSale(ctx context.Context, d *common.Deps, in pos.SaleInput) {
	reason := "sale"
	if in.SaleType == "return" {
		reason = "refund"
	}
	for _, l := range in.Lines {
		delta := l.Qty
		if in.SaleType == "sale" {
			delta = -delta
		}
		publishStockAdjusted(ctx, d, plugins.StockAdjustedEvent{
			ItemID:    l.ItemID,
			VariantID: l.VariantID,
			SKU:       l.SKU,
			DeltaQty:  delta,
			Reason:    reason,
			Location:  l.LocationID,
		})
	}
}

// stockMovementReason maps a stock_movements type to the stock.adjusted reason.
func stockMovementReason(movementType string) string {
	switch movementType {
	case "receive":
		return "received"
	case "adjust":
		return "adjustment"
	default:
		return movementType
	}
}
