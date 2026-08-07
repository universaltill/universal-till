package pages

import (
	"context"
	"encoding/json"
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
	"github.com/universaltill/universal-till/internal/plugins"
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

// refundLinePool computes, per refund-line key, the TRUE quantity still
// refundable: everything originally sold under that key (summed across
// every original sale line sharing it — e.g. the same item scanned twice
// as separate lines) minus what real returns have already taken.
func refundLinePool(lines []data.SaleDetailLine, returned map[string]float64) map[string]float64 {
	pool := map[string]float64{}
	for _, l := range lines {
		pool[data.RefundLineKey(l.ItemID, l.VariantID, l.UnitPrice)] += l.Qty
	}
	for key, r := range returned {
		pool[key] -= r
	}
	for key, v := range pool {
		if v < 0 {
			pool[key] = 0
		}
	}
	return pool
}

// refundableLines computes what's left to give back per line.
func refundableLines(detail data.SaleDetail, returned map[string]float64) []refundLineView {
	pool := refundLinePool(detail.Lines, returned)
	var out []refundLineView
	for i, l := range detail.Lines {
		key := data.RefundLineKey(l.ItemID, l.VariantID, l.UnitPrice)
		remaining := l.Qty
		if remaining > pool[key] {
			remaining = pool[key]
		}
		out = append(out, refundLineView{
			Index: i, Name: l.Name, SKU: l.SKU, UnitPrice: l.UnitPrice,
			Sold: l.Qty, Remaining: remaining,
		})
		// Multiple original lines sharing a key split the same remaining
		// pool; charge this line's view against it so the page never
		// offers more than is truly refundable overall — but starting
		// from the TRUE pool (total sold under the key minus real
		// returns), not from each line's own quantity naively subtracted,
		// which under-counted whenever a prior partial return already
		// existed (confirmed live via a hand-traced case: two lines of
		// qty 2 sharing a key, 1 already returned, displayed only 1 unit
		// remaining total instead of the true 3).
		pool[key] -= remaining
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
		// Same TRUE-pool accounting as refundableLines (the page's display
		// computation) — using each line's own l.Qty - returned[key] here
		// under-counted whenever multiple original lines shared a key AND
		// a prior partial return already existed, wrongly rejecting a
		// legitimate combined request (e.g. lines of qty 2+2, 1 already
		// returned, true pool 3: requesting 2 on line 0 and 1 on line 1 is
		// exactly correct but the old check rejected line 0 alone as
		// "only 1 left").
		pool := refundLinePool(detail.Lines, returned)
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
			remaining := pool[key]
			if qty > remaining+1e-9 {
				http.Error(w, fmt.Sprintf("line %q: only %.3g left to refund", l.Name, remaining), http.StatusConflict)
				return
			}
			pool[key] -= qty
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

		// Payment-provider refund (payment-provider contract): if the refund
		// method belongs to a payment plugin that hooks `payment.<key>.refund`,
		// it gets a BLOCKING call BEFORE the return is recorded — the provider
		// must actually send the money back (e.g. refund the Stripe charge),
		// and a failed provider refund must stop the return. No subscriber =
		// no gate (cash and hook-less methods behave as before).
		if blocked := blockingPaymentEvent(r.Context(), d, method, "refund", map[string]any{
			"method":           method,
			"amount":           refundTotal.Minor(),
			"currency":         detail.Currency,
			"original_sale_id": detail.ID,
			"original_receipt": detail.ReceiptNo,
		}); blocked != nil {
			http.Error(w, "provider refund failed for "+method+": "+blocked.Error(), http.StatusPaymentRequired)
			return
		}
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
		// Mirror the restock to inventory connectors (best-effort, non-blocking).
		publishStockAdjustedForSale(r.Context(), d, saleInput)
		// A replica's refund is a journaled sale like any other (ADR-0011
		// D3) — nudge the push loop the same way a tender does (ut-docs#404,
		// ADR-0036) so the primary hears about the restock in seconds, not
		// the next 30s tick. No-op on a primary/single till.
		if d.SyncPrimaryURL(r.Context()) != "" {
			d.RequestSyncPush()
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

// blockingPaymentEvent publishes `payment.<key>.<suffix>` for the method's
// owning payment plugin and BLOCKS on the result. Returns nil when the method
// has no payment entry, no subscriber, or the plugin approves; returns the
// plugin's error when it declines. Shared by the tender authorize gate and
// the refund gate — the two blocking legs of the payment-provider contract.
func blockingPaymentEvent(ctx context.Context, d *common.Deps, method, suffix string, payload map[string]any) error {
	_, err := blockingPaymentEventWithResponse(ctx, d, method, suffix, payload)
	return err
}

// blockingPaymentEventWithResponse behaves exactly like blockingPaymentEvent
// but also returns the responding plugin's raw response instead of
// discarding it — the tender authorize gate uses this to read back
// plugin-reported data (e.g. a reader-captured tip amount) alongside the
// approve/decline verdict. resp is nil in every case blockingPaymentEvent
// would return nil with nothing to report (no entry, no subscriber).
func blockingPaymentEventWithResponse(ctx context.Context, d *common.Deps, method, suffix string, payload map[string]any) (json.RawMessage, error) {
	entries, err := data.NewPluginRepo(d.Db).ListPaymentEntries(ctx)
	if err != nil || len(entries) == 0 {
		return nil, nil
	}
	for _, e := range entries {
		if e.EntryKey != method || e.TriggerEvent == "" {
			continue
		}
		event := strings.TrimSuffix(e.TriggerEvent, ".requested") + "." + suffix
		bus := plugins.SharedBus(d.Db)
		if !bus.HasSubscribers(event) {
			return nil, nil
		}
		payload["plugin_id"] = e.PluginID
		resp, err := bus.PublishAuthorize(ctx, event, payload)
		if err != nil {
			return nil, err
		}
		return resp, nil
	}
	return nil, nil
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
