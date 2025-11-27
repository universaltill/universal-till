package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

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
			AllowNegative bool   `json:"allowNegativeInventory,omitempty"`
			Note          string `json:"note,omitempty"`
		}
		var in In
		_ = json.NewDecoder(r.Body).Decode(&in)

		lines := d.engine.Lines()
		if len(lines) == 0 {
			http.Error(w, "no items in basket", http.StatusBadRequest)
			return
		}
		if len(in.Payments) == 0 {
			http.Error(w, "payments required", http.StatusBadRequest)
			return
		}

		locID, err := ensureStockLocation(r.Context(), d.db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var payments []pos.PaymentInput
		for _, p := range in.Payments {
			if p.Method == "" || p.Amount <= 0 {
				http.Error(w, "invalid payment", http.StatusBadRequest)
				return
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

		registerID := in.RegisterID
		if registerID == "" {
			if regID, err := ensureRegister(r.Context(), d.db); err == nil {
				registerID = regID
			}
		}

		_, err = pos.CompleteSale(r.Context(), d.db, pos.SaleInput{
			SaleType:               "sale",
			Currency:               d.state.Currency,
			TaxInclusive:           d.state.TaxInclusive,
			SaleDiscount:           in.Discount,
			Lines:                  saleLines,
			Payments:               payments,
			Note:                   in.Note,
			RegisterID:             registerID,
			CashierID:              in.CashierID,
			CustomerID:             in.CustomerID,
			AllowNegativeInventory: in.AllowNegative,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		d.engine.Reset()
		b, _ := d.engine.Scan("")
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		_ = basketView.Render(w, b)
	})
}

// ensureStockLocation returns an existing location id or creates a default one.
func ensureStockLocation(ctx context.Context, db *sql.DB) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT id FROM stock_locations ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read stock_locations: %w", err)
	}
	id = "loc-default"
	if _, err := db.ExecContext(ctx, `INSERT INTO stock_locations(id, name) VALUES(?,?)`, id, "Default"); err != nil {
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
