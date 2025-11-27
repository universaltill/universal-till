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
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerPOSAPI(mux *http.ServeMux, d *deps) {
	mux.HandleFunc("/api/pos/scan", func(w http.ResponseWriter, r *http.Request) {
		code := ""
		qty := 1
		if r.Header.Get("Content-Type") == "application/json" {
			type In struct {
				Code string `json:"code"`
				Qty  int    `json:"qty"`
			}
			var in In
			_ = json.NewDecoder(r.Body).Decode(&in)
			code = in.Code
			if in.Qty > 0 {
				qty = in.Qty
			}
		} else {
			_ = r.ParseForm()
			code = r.Form.Get("code")
			if q := r.Form.Get("qty"); q != "" {
				if v, err := strconv.Atoi(q); err == nil && v > 0 {
					qty = v
				}
			}
		}
		b, _ := d.engine.ScanQty(code, qty)
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
		d.engine.Remove(code)
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		b := d.engine.Basket()
		_ = basketView.Render(w, b)
	})

	// Clear toast for OOB swap.
	mux.HandleFunc("/ui/clear-toast", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<div id="toast-error"></div>`)
	})

	// Reset basket for new customer.
	mux.HandleFunc("/api/pos/reset", func(w http.ResponseWriter, r *http.Request) {
		d.engine.Reset()
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		b, _ := d.engine.Scan("")
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

		lines := d.engine.Lines()
		if len(lines) == 0 {
			http.Error(w, "no items in basket", http.StatusBadRequest)
			return
		}

		locID, err := ensureStockLocation(r.Context(), d.db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var saleLines []pos.SaleLineInput
		for _, l := range lines {
			taxBP := l.TaxRateBP
			if taxBP == 0 {
				taxBP = d.state.TaxRatePct * 100
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
				LineDiscount:       0,
				LocationID:         locID,
			})
		}

		var payments []pos.PaymentInput
		for _, p := range in.Payments {
			if p.Method == "" || p.Amount <= 0 {
				continue
			}
			if err := ensurePaymentMethod(r.Context(), d.db, p.Method); err != nil {
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
					if err := ensurePaymentMethod(r.Context(), d.db, method); err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					payments = append(payments, pos.PaymentInput{
						MethodID: method,
						Amount:   amount,
						Currency: d.state.Currency,
					})
				}
			}
		}
		// compute totals for receipt and fallback payment
		subtotal, taxTotal := int64(0), int64(0)
		for i := range saleLines {
			lineBase := pos.AmountForQuantity(saleLines[i].UnitPrice, saleLines[i].Qty)
			lineNet := lineBase - saleLines[i].LineDiscount
			lineTax, _ := pos.ComputeTaxBasisPoints(lineNet, saleLines[i].TaxRateBasisPoints, d.state.TaxInclusive)
			subtotal += lineNet
			taxTotal += lineTax
		}
		total := subtotal - in.Discount
		if !d.state.TaxInclusive {
			total += taxTotal
		}
		if total < 0 {
			total = 0
		}
		if len(payments) == 0 {
			payments = append(payments, pos.PaymentInput{
				MethodID: "cash",
				Amount:   total,
				Currency: d.state.Currency,
			})
		}
		for i := range payments {
			if payments[i].Amount <= 0 {
				payments[i].Amount = total
			}
		}

		registerID := in.RegisterID
		if registerID == "" {
			if regID, err := ensureRegister(r.Context(), d.db); err == nil {
				registerID = regID
			}
		}

		allowNegative := d.state.AllowNegativeInventory
		if in.AllowNegative != nil {
			allowNegative = *in.AllowNegative
		}

		cashierID := in.CashierID
		if cashierID == "" {
			if cid, err := ensureUser(r.Context(), d.db); err == nil {
				cashierID = cid
			}
		}

		saleID, err := pos.CompleteSale(r.Context(), d.db, pos.SaleInput{
			SaleType:               "sale",
			Currency:               d.state.Currency,
			TaxInclusive:           d.state.TaxInclusive,
			SaleDiscount:           in.Discount,
			Lines:                  saleLines,
			Payments:               payments,
			Note:                   in.Note,
			RegisterID:             registerID,
			CashierID:              cashierID,
			CustomerID:             in.CustomerID,
			AllowNegativeInventory: allowNegative,
			ActorID:                cashierID,
		})
		if err != nil {
			if strings.Contains(err.Error(), "insufficient stock") {
				locale := httpx.ResolveLocale(w, r)
				funcs := httpx.FuncsFor(locale)
				b := d.engine.Basket()
				basketView, _ := ui.NewBasketView(funcs)
				var buf bytes.Buffer
				_ = basketView.Render(&buf, b)
				buf.WriteString(`<div id="toast-container" hx-swap-oob="true"><div class="toast toast-error">` + err.Error() + `</div><script>setTimeout(function(){var t=document.getElementById('toast-container');if(t){t.innerHTML='';}},2000);</script></div>`)
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(buf.Bytes())
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		d.engine.Reset()

		// Render receipt JSON if requested
		if r.Header.Get("Accept") == "application/json" {
			resp := map[string]any{
				"saleId":   saleID,
				"total":    total,
				"payments": payments,
				"note":     in.Note,
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		receiptHTML, _ := renderReceipt(funcs, saleID, saleLines, subtotal, taxTotal, total, d.state.TaxInclusive)

		b, _ := d.engine.Scan("")
		basketView, _ := ui.NewBasketView(funcs)
		var basketBuf bytes.Buffer
		_ = basketView.Render(&basketBuf, b)

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div id="basket">%s%s</div>`, receiptHTML, basketBuf.String())
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
		if err := pos.UpdateSaleStatus(r.Context(), d.db, in.SaleID, in.Status, "", in.Reason); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
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

func renderReceipt(funcs template.FuncMap, receiptNo string, lines []pos.SaleLineInput, subtotal, taxTotal, total int64, taxInclusive bool) (string, error) {
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
		if taxInclusive {
			lineTax = 0 // already in net; only show totals separately
		}
		rlines = append(rlines, receiptLine{
			Name:          l.Name,
			Qty:           int(l.Qty),
			TotalAfterTax: lineNet + lineTax,
		})
	}
	data := map[string]any{
		"ReceiptNo": receiptNo,
		"Lines":     rlines,
		"Subtotal":  subtotal,
		"TaxTotal":  taxTotal,
		"Total":     total,
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "receipt", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
