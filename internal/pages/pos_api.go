package pages

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"text/template"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerPOSAPI(mux *http.ServeMux, d *common.Deps) {
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
		customerID := d.Engine.CustomerID()

		// If the scan is a customer barcode, attach and return current basket.
		if custID, custName, ok := lookupCustomer(r.Context(), d.Db, code); ok {
			d.Engine.SetCustomer(custID, custName)
			funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
			basketView, _ := ui.NewBasketView(funcs)
			b := d.Engine.Basket()
			b.ToastMessage = fmt.Sprintf("Customer %s linked", custName)
			_ = basketView.Render(w, b)
			return
		}

		customerID = d.Engine.CustomerID()
		if promoType, value, ok := promoFromDB(r.Context(), d.Db, customerID, code); ok {
			if promoType == "percent" {
				d.Engine.SetDiscountPercent(value)
			} else {
				d.Engine.SetDiscount(value)
			}
			funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
			basketView, _ := ui.NewBasketView(funcs)
			b := d.Engine.Basket()
			b.ToastMessage = fmt.Sprintf("Promotion %s applied", code)
			_ = basketView.Render(w, b)
			return
		}
		b, _ := d.Engine.ScanQty(code, in.Qty)
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		_ = basketView.Render(w, b)
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
		d.Engine.UpdateLine(code, qty, discount)
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
		d.Engine.SetDiscount(discount)
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
		}
		var in In
		_ = json.NewDecoder(r.Body).Decode(&in)

		lines := d.Engine.Lines()
		if len(lines) == 0 {
			http.Error(w, "no items in basket", http.StatusBadRequest)
			return
		}

		locID, err := ensureStockLocation(r.Context(), d.Db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var saleLines []pos.SaleLineInput
		for _, l := range lines {
			taxBP := l.TaxRateBP
			if taxBP == 0 {
				taxBP = d.State.TaxRatePct * 100
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
			if err := ensurePaymentMethod(r.Context(), d.Db, p.Method); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			payments = append(payments, pos.PaymentInput{
				MethodID:    p.Method,
				Amount:      p.Amount,
				Currency:    p.Currency,
				Reference:   p.Reference,
				ChangeGiven: p.Change,
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
					if err := ensurePaymentMethod(r.Context(), d.Db, method); err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					payments = append(payments, pos.PaymentInput{
						MethodID: method,
						Amount:   amount,
						Currency: d.State.Currency,
					})
				}
			}
		}
		// compute totals for receipt and fallback payment
		subtotal, taxTotal := int64(0), int64(0)
		for i := range saleLines {
			lineBase := pos.AmountForQuantity(saleLines[i].UnitPrice, saleLines[i].Qty)
			lineNet := lineBase - saleLines[i].LineDiscount
			lineTax, _ := pos.ComputeTaxBasisPoints(lineNet, saleLines[i].TaxRateBasisPoints, d.State.TaxInclusive)
			subtotal += lineNet
			taxTotal += lineTax
		}
		total := subtotal - in.Discount
		if !d.State.TaxInclusive {
			total += taxTotal
		}
		if total < 0 {
			total = 0
		}
		if len(payments) == 0 {
			payments = append(payments, pos.PaymentInput{
				MethodID: "cash",
				Amount:   total,
				Currency: d.State.Currency,
			})
		}
		for i := range payments {
			if payments[i].Amount <= 0 {
				payments[i].Amount = total
			}
		}

		registerID := in.RegisterID
		if registerID == "" {
			if regID, err := ensureRegister(r.Context(), d.Db); err == nil {
				registerID = regID
			}
		}

		allowNegative := d.State.AllowNegativeInventory
		if in.AllowNegative != nil {
			allowNegative = *in.AllowNegative
		}

		cashierID := in.CashierID
		if cashierID == "" {
			if cid, err := ensureUser(r.Context(), d.Db); err == nil {
				cashierID = cid
			}
		}

		discount := in.Discount
		if discount == 0 {
			discount = d.Engine.SaleDiscount()
		}

		customerID := in.CustomerID
		if customerID == "" {
			customerID = d.Engine.CustomerID()
		}

		saleID, err := pos.CompleteSale(r.Context(), d.Db, pos.SaleInput{
			SaleType:               "sale",
			Currency:               d.State.Currency,
			TaxInclusive:           d.State.TaxInclusive,
			SaleDiscount:           discount,
			Lines:                  saleLines,
			Payments:               payments,
			Note:                   in.Note,
			RegisterID:             registerID,
			CashierID:              cashierID,
			CustomerID:             customerID,
			AllowNegativeInventory: allowNegative,
			ActorID:                cashierID,
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

		// load receipt_no and totals from DB for rendering
		var receiptNo string
		var dbSubtotal, dbTax, dbTotal int64
		_ = d.Db.QueryRowContext(r.Context(), `SELECT receipt_no, subtotal, tax_total, total FROM sales WHERE id = ?`, saleID).
			Scan(&receiptNo, &dbSubtotal, &dbTax, &dbTotal)
		if receiptNo == "" {
			receiptNo = saleID
		}

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

		receiptHTML, _ := renderReceipt(funcs, receiptNo, saleLines, dbSubtotal, dbTax, dbTotal, d.State.TaxInclusive, discount, discountType, discountRaw)

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<div id="basket">%s</div>`, receiptHTML)
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

// promoFromDB looks up promotions table with optional customer targeting and active window.
// It returns the promo type (amount|percent) and numeric value (minor units or basis points).
func promoFromDB(ctx context.Context, db *sql.DB, customerID string, code string) (string, int64, bool) {
	row := db.QueryRowContext(ctx, `
SELECT type, value
FROM promotions
WHERE code = ?
  AND is_active = 1
  AND (customer_id IS NULL OR customer_id = ?)
  AND (starts_at IS NULL OR datetime(starts_at) <= CURRENT_TIMESTAMP)
  AND (ends_at IS NULL OR datetime(ends_at) >= CURRENT_TIMESTAMP)
LIMIT 1
`, strings.TrimSpace(code), nullIfEmpty(customerID))
	var pType string
	var value int64
	if err := row.Scan(&pType, &value); err != nil {
		return "", 0, false
	}
	if value <= 0 {
		return "", 0, false
	}
	if pType == "" {
		pType = "amount"
	}
	return pType, value, true
}

func lookupCustomer(ctx context.Context, db *sql.DB, code string) (string, string, bool) {
	c := strings.TrimSpace(code)
	if c == "" {
		return "", "", false
	}
	row := db.QueryRowContext(ctx, `
SELECT id, name FROM customers
WHERE id = ? OR loyalty_no = ? OR phone = ?
LIMIT 1
`, c, c, c)
	var id, name string
	if err := row.Scan(&id, &name); err != nil {
		return "", "", false
	}
	return id, name, true
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ensureStockLocation returns an existing location id or creates a default one.
func ensureStockLocation(ctx context.Context, db *sql.DB) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT id FROM stock_locations WHERE name = 'Main' OR id = 'loc_main' ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read stock_locations: %w", err)
	}
	id = "loc_main"
	if _, err := db.ExecContext(ctx, `INSERT INTO stock_locations(id, name) VALUES(?,?)`, id, "Main"); err != nil {
		return "", fmt.Errorf("create default location: %w", err)
	}
	return id, nil
}

// ensurePaymentMethod upserts a minimal payment method to satisfy FK.
func ensurePaymentMethod(ctx context.Context, db *sql.DB, id string) error {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT 1 FROM payment_methods WHERE id = ? AND is_active = 1`, id).Scan(&exists); err == nil {
		return nil
	}
	_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES (?, ?, 'cash', 1)`, id, id)
	return err
}

// ensureRegister returns an existing register or creates a default one.
func ensureRegister(ctx context.Context, db *sql.DB) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT id FROM registers WHERE is_active = 1 ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read registers: %w", err)
	}
	id = "reg-default"
	if _, err := db.ExecContext(ctx, `INSERT INTO registers (id, name, is_active) VALUES (?, ?, 1)`, id, "Default Register"); err != nil {
		return "", fmt.Errorf("create default register: %w", err)
	}
	return id, nil
}

// ensureUser returns a default cashier user if none exists.
func ensureUser(ctx context.Context, db *sql.DB) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE is_active = 1 ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read users: %w", err)
	}
	id = "cashier-default"
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, display_name, role, is_active) VALUES (?, ?, ?, 'cashier', 1)`, id, "cashier", "Default Cashier"); err != nil {
		return "", fmt.Errorf("create default user: %w", err)
	}
	return id, nil
}

type receiptLine struct {
	Name          string
	Qty           int
	TotalAfterTax int64
}

func renderReceipt(funcs template.FuncMap, receiptNo string, lines []pos.SaleLineInput, subtotal, taxTotal, total int64, taxInclusive bool, saleDiscount int64, saleDiscountType string, saleDiscountRaw int64) (string, error) {
	t, err := template.New("receipt.html").Funcs(funcs).ParseFiles(
		"web/ui/partials/receipt.html",
	)
	if err != nil {
		return "", err
	}
	var rlines []receiptLine
	for _, l := range lines {
		lineBase := pos.AmountForQuantity(l.UnitPrice, l.Qty)
		lineNet := lineBase - l.LineDiscount
		lineTax, _ := pos.ComputeTaxBasisPoints(lineNet, l.TaxRateBasisPoints, taxInclusive)
		lineTotal := lineNet
		if !taxInclusive {
			lineTotal += lineTax
		}
		rlines = append(rlines, receiptLine{
			Name:          l.Name,
			Qty:           int(l.Qty),
			TotalAfterTax: lineTotal,
		})
	}
	data := map[string]any{
		"ReceiptNo":        receiptNo,
		"Lines":            rlines,
		"Subtotal":         subtotal,
		"TaxTotal":         taxTotal,
		"SaleDiscount":     saleDiscount,
		"SaleDiscountType": saleDiscountType,
		"SaleDiscountRaw":  saleDiscountRaw,
		"Total":            total,
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "receipt", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
