package pages

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
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
)

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

		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		render := func(b *pos.Basket) {
			_ = basketView.Render(w, b)
		}

		if code == "" {
			b := d.Engine.Basket()
			b.ToastMessage = "Scan a barcode"
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
				b.ToastMessage = fmt.Sprintf("Customer %s linked", custName)
				render(&b)
				return
			}
			b := d.Engine.Basket()
			b.ToastMessage = "Customer not found"
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
			b.ToastMessage = fmt.Sprintf("Promotion %s applied", code)
			render(&b)
			return
		}

		b := d.Engine.Basket()
		b.ToastMessage = "Item not found"
		render(&b)
	})

	// Remove a line by SKU/code.
	mux.HandleFunc("/api/pos/remove", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := strings.TrimSpace(r.Form.Get("code"))
		if code == "" {
			http.Error(w, "code required", http.StatusBadRequest)
			return
		}
		d.Engine.Remove(code)
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		b := d.Engine.Basket()
		_ = basketView.Render(w, b)
	})

	// Update line qty/discount (htmx-friendly)
	mux.HandleFunc("/api/pos/line", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := strings.TrimSpace(r.Form.Get("code"))
		if code == "" {
			http.Error(w, "code required", http.StatusBadRequest)
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
		d.Engine.UpdateLine(code, qty, money.FromMinor(discount))
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

	// Clear toast for OOB swap.
	mux.HandleFunc("/ui/clear-toast", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<div id="toast-error"></div>`)
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
			taxBP := l.TaxRateBP
			if taxBP == 0 {
				taxBP = d.CurrentState().TaxRatePct * 100
				if taxBP == 0 {
					taxBP = 2000 // fallback to 20% if missing to avoid zero-tax totals
				}
			}
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
		total := subtotal.Sub(discount)
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

		saleID, err := pos.CompleteSale(r.Context(), d.Db, pos.SaleInput{
			SaleType:               "sale",
			Currency:               d.CurrentState().Currency,
			TaxInclusive:           d.CurrentState().TaxInclusive,
			SaleDiscount:           discount,
			Lines:                  saleLines,
			Payments:               payments,
			Note:                   in.Note,
			RegisterID:             registerID,
			CashierID:              cashierID,
			CustomerID:             customerID,
			AllowNegativeInventory: allowNegative,
			ActorID:                cashierID,
			Offline:                offline,
		})
		if err != nil {
			if strings.Contains(err.Error(), "insufficient stock") {
				locale := httpx.ResolveLocale(w, r)
				funcs := httpx.FuncsFor(locale)
				b := d.Engine.Basket()
				basketView, _ := ui.NewBasketView(funcs)
				var buf bytes.Buffer
				_ = basketView.Render(&buf, b)
				html := buf.String()
				var out bytes.Buffer
				// prepend toast inside the basket container so it renders in-place
				out.WriteString(`<div class="toast toast-error" id="toast-error">` + err.Error() + `</div>`)
				out.WriteString(html)
				out.WriteString(`<script>setTimeout(function(){var t=document.getElementById('toast-error');if(t){t.remove();}},2000);</script>`)
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(out.Bytes())
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		d.Engine.Reset()

		// Plugin-provided tender methods: publish each entry's trigger_event so
		// the owning plugin can react (charge a terminal, show a QR, …).
		if entries, err := data.NewPluginRepo(d.Db).ListPaymentEntries(r.Context()); err == nil && len(entries) > 0 {
			byMethod := map[string]data.PaymentEntryRow{}
			for _, e := range entries {
				byMethod[e.EntryKey] = e
			}
			bus := plugins.SharedBus(d.Db)
			for _, p := range payments {
				if e, ok := byMethod[p.MethodID]; ok && e.TriggerEvent != "" {
					_, _ = bus.Publish(r.Context(), e.TriggerEvent, map[string]any{
						"sale_id":   saleID,
						"method":    p.MethodID,
						"amount":    p.Amount.Minor(),
						"reference": p.Reference,
						"plugin_id": e.PluginID,
					})
				}
			}
		}

		// load receipt_no and totals from DB for rendering
		receiptNo, dbSubtotal, dbTax, dbTotal, _ := repo.SaleTotals(r.Context(), saleID)
		if receiptNo == "" {
			receiptNo = saleID
		}

		// Silent receipt print (docs: receipt-printing.md) — fired async,
		// never blocks or fails the tender.
		printReceiptAsync(d, receiptNo, getSessionUserID(r))

		// Render receipt JSON if requested
		if r.Header.Get("Accept") == "application/json" {
			resp := map[string]any{
				"saleId":    saleID,
				"receiptNo": receiptNo,
				"total":     dbTotal,
				"payments":  payments,
				"note":      in.Note,
			}
			_ = json.NewEncoder(w).Encode(resp)
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
		receiptHTML, renderErr := renderReceipt(funcs, receiptNo, saleLines, payments, dbSubtotal, dbTax, dbTotal, d.CurrentState().TaxInclusive, discount.Minor(), discountType, discountRaw, legalBlocks, printerUnavailable)
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
		if err := pos.UpdateSaleStatus(r.Context(), d.Db, in.SaleID, in.Status, "", in.Reason); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

type receiptLine struct {
	Name          string
	Qty           int
	TotalAfterTax int64
}

type receiptPayment struct {
	Method    string
	Applied   int64
	Change    int64
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

func renderReceipt(funcs template.FuncMap, receiptNo string, lines []pos.SaleLineInput, payments []pos.PaymentInput, subtotal, taxTotal, total int64, taxInclusive bool, saleDiscount int64, saleDiscountType string, saleDiscountRaw int64, legalBlocks []receiptLegalBlock, printerUnavailable bool) (string, error) {
	t, err := template.New("receipt.html").Funcs(funcs).ParseFiles(
		"web/ui/partials/receipt.html",
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
		rlines = append(rlines, receiptLine{
			Name:          l.Name,
			Qty:           int(l.Qty),
			TotalAfterTax: lineTotal.Minor(),
		})
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
