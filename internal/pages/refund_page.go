package pages

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// refundLineView is one refundable line on the /refund page.
type refundLineView struct {
	Index     int
	Name      string
	SKU       string
	UnitPrice int64
	Sold      float64
	Remaining float64
}

// saleIsTaxInclusive infers the original sale's pricing mode from its own
// header arithmetic (settings may have changed since the sale happened):
// inclusive keeps total = subtotal − discount; exclusive adds tax on top.
func saleIsTaxInclusive(d data.SaleDetail) bool {
	if d.TaxTotal == 0 {
		return false // both modes agree; exclusive math is the identity
	}
	return d.Total == d.Subtotal-d.DiscountTotal
}

// refundableLines computes what's left to give back per line.
func refundableLines(detail data.SaleDetail, returned map[string]float64) []refundLineView {
	var out []refundLineView
	for i, l := range detail.Lines {
		key := data.RefundLineKey(l.ItemID, l.VariantID, l.UnitPrice)
		remaining := l.Qty - returned[key]
		if remaining < 0 {
			remaining = 0
		}
		out = append(out, refundLineView{
			Index: i, Name: l.Name, SKU: l.SKU, UnitPrice: l.UnitPrice,
			Sold: l.Qty, Remaining: remaining,
		})
		// Multiple original lines sharing a key split the same remaining
		// pool; charge this line's view against it so the page never
		// offers more than is truly refundable overall.
		returned[key] += remaining
	}
	return out
}

// registerRefund mounts the refund screen + API (docs: refunds.md, G27/G28).
func registerRefund(mux *http.ServeMux, d *common.Deps, svc *auth.Service) {
	repo := data.NewPOSRepo(d.Db)
	authOff := auth.Disabled(os.Getenv("UT_AUTH"))

	mux.HandleFunc("GET /refund/{receipt}", func(w http.ResponseWriter, r *http.Request) {
		receipt := r.PathValue("receipt")
		detail, found, err := repo.GetSaleDetail(r.Context(), receipt)
		if err != nil || !found {
			http.Redirect(w, r, "/journal", http.StatusSeeOther)
			return
		}
		if detail.SaleType != "sale" || detail.Status != "completed" {
			http.Redirect(w, r, "/journal/"+receipt, http.StatusSeeOther)
			return
		}
		returned, err := repo.ReturnedQuantities(r.Context(), detail.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		methods := []string{"cash"}
		for _, p := range detail.Payments {
			if p.Method != "cash" {
				methods = append(methods, p.Method)
			}
		}
		httpx.Render("ui/pages/refund.html", map[string]any{
			"title":     "Refund",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.Menu,
			"Sale":      detail,
			"Lines":     refundableLines(detail, returned),
			"Methods":   methods,
			"AuthOff":   authOff,
		})(w, r)
	})

	mux.HandleFunc("POST /api/refund", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		receipt := strings.TrimSpace(r.Form.Get("receipt"))
		detail, found, err := repo.GetSaleDetail(r.Context(), receipt)
		if err != nil || !found || detail.SaleType != "sale" {
			http.Error(w, "sale not found", http.StatusNotFound)
			return
		}

		// Manager approval; the PIN owner is the audit actor (pos-auth).
		actorID := getSessionUserID(r)
		if !authOff {
			approver, err := svc.AuthorizeManager(r.Context(), strings.TrimSpace(r.Form.Get("manager_pin")))
			if err != nil {
				status := http.StatusForbidden
				if errors.Is(err, auth.ErrLockedOut) {
					status = http.StatusTooManyRequests
				}
				http.Error(w, "manager PIN required", status)
				return
			}
			actorID = approver.ID
		}

		returned, err := repo.ReturnedQuantities(r.Context(), detail.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		locID, err := repo.EnsureStockLocation(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Collect requested quantities; enforce the double-refund guard.
		var lines []pos.SaleLineInput
		var refundGross, origGross int64
		for _, l := range detail.Lines {
			origGross += int64(float64(l.UnitPrice) * l.Qty)
		}
		for i, l := range detail.Lines {
			raw := strings.TrimSpace(r.Form.Get("qty_" + strconv.Itoa(i)))
			if raw == "" || raw == "0" {
				continue
			}
			qty, err := strconv.ParseFloat(raw, 64)
			if err != nil || qty <= 0 {
				http.Error(w, fmt.Sprintf("invalid quantity for line %d", i+1), http.StatusBadRequest)
				return
			}
			key := data.RefundLineKey(l.ItemID, l.VariantID, l.UnitPrice)
			remaining := l.Qty - returned[key]
			if qty > remaining+1e-9 {
				http.Error(w, fmt.Sprintf("line %q: only %.3g left to refund", l.Name, remaining), http.StatusConflict)
				return
			}
			returned[key] += qty
			// Prorate the line discount for partial-quantity refunds.
			share := qty / l.Qty
			lineDiscount := int64(float64(l.LineDiscount) * share)
			lines = append(lines, pos.SaleLineInput{
				ItemID: l.ItemID, VariantID: l.VariantID, SKU: l.SKU, Name: l.Name,
				Qty: qty, UnitPrice: money.FromMinor(l.UnitPrice),
				TaxRateBasisPoints: l.TaxRateBP,
				LineDiscount:       money.FromMinor(lineDiscount),
				LocationID:         locID,
			})
			refundGross += int64(float64(l.UnitPrice) * qty)
		}
		if len(lines) == 0 {
			http.Error(w, "select at least one item to refund", http.StatusBadRequest)
			return
		}

		// Whole-sale discount prorated by the refunded share of the sale.
		var saleDiscount int64
		if detail.DiscountTotal > 0 && origGross > 0 {
			saleDiscount = detail.DiscountTotal * refundGross / origGross
		}

		method := strings.TrimSpace(r.Form.Get("method"))
		if method == "" {
			method = "cash"
		}
		if err := repo.EnsurePaymentMethod(r.Context(), method); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		inclusive := saleIsTaxInclusive(detail)
		// Engine computes the refund total from the same inputs as the
		// original sale; the payment must cover it exactly.
		refundTotal := computeRefundTotal(lines, money.FromMinor(saleDiscount), inclusive)
		saleInput := pos.SaleInput{
			SaleType:               "return",
			CashierID:              actorID,
			ActorID:                actorID,
			Currency:               detail.Currency,
			TaxInclusive:           inclusive,
			SaleDiscount:           money.FromMinor(saleDiscount),
			Lines:                  lines,
			Payments:               []pos.PaymentInput{{MethodID: method, Amount: refundTotal, Currency: detail.Currency}},
			OriginalSaleID:         detail.ID,
			Note:                   "refund of " + detail.ReceiptNo,
			AllowNegativeInventory: true, // returns only add stock back
		}
		saleID, err := pos.CompleteSale(r.Context(), d.Db, saleInput)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		newReceipt, _, _, _, _ := repo.SaleTotals(r.Context(), saleID)
		_ = repo.InsertAudit(r.Context(), nil, actorID, "sale", newReceipt, "refund",
			map[string]any{"original": detail.ReceiptNo, "amount": refundTotal.Minor(), "method": method},
			time.Now().UTC().Format(time.RFC3339), "")
		printReceiptAsync(d, newReceipt, actorID)
		// Invoiced sale? A credit note follows automatically (G31).
		maybeIssueCreditNote(r.Context(), d, newReceipt, detail.ID, actorID)
		w.Header().Set("HX-Redirect", "/journal/"+newReceipt)
		w.WriteHeader(http.StatusOK)
	})
}

// computeRefundTotal mirrors the engine's total math so the refund payment
// covers the return exactly (CompleteSale enforces coverage).
func computeRefundTotal(lines []pos.SaleLineInput, saleDiscount money.Money, inclusive bool) money.Money {
	var subtotal, tax money.Money
	for _, l := range lines {
		net := pos.AmountForQuantity(l.UnitPrice, l.Qty).Sub(l.LineDiscount)
		t, _ := pos.ComputeTaxBasisPoints(net, l.TaxRateBasisPoints, inclusive)
		subtotal = subtotal.Add(net)
		tax = tax.Add(t)
	}
	total := subtotal.Sub(saleDiscount)
	if !inclusive {
		total = total.Add(tax)
	}
	if total.IsNegative() {
		return 0
	}
	return total
}
