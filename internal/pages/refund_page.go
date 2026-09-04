package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
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
	// OrderType (ut-docs#1181, ADR-0073): the line's own mode, so two rows
	// of the same product at the same price but different modes/tax rates
	// are distinguishable on the refund screen.
	OrderType string
}

// saleIsTaxInclusive infers the original sale's pricing mode from its own
// header arithmetic (settings may have changed since the sale happened):
// inclusive keeps total = subtotal − discount; exclusive adds tax on top.
// The inference itself is pos.InferTaxInclusive (extracted with the shared
// VAT banding, ut-docs#1003) so the day-close band computation — which
// reads raw sale rows, not data.SaleDetail — can never drift from it; see
// its doc comment for why the service charge and the voucher issue total
// (ut-docs#1008 review F1) participate.
func saleIsTaxInclusive(d data.SaleDetail) bool {
	return pos.InferTaxInclusive(d.Subtotal, d.DiscountTotal, d.TaxTotal, d.Total, d.ServiceCharge, d.VoucherIssueTotal)
}

// refundLinePool computes, per refund-line key, the TRUE quantity still
// refundable: everything originally sold under that key (summed across
// every original sale line sharing it — e.g. the same item scanned twice
// as separate lines) minus what real returns have already taken.
func refundLinePool(lines []data.SaleDetailLine, returned map[string]float64) map[string]float64 {
	pool := map[string]float64{}
	for _, l := range lines {
		pool[data.RefundLineKey(l.ItemID, l.VariantID, l.UnitPrice, l.OrderType)] += l.Qty
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
		key := data.RefundLineKey(l.ItemID, l.VariantID, l.UnitPrice, l.OrderType)
		remaining := l.Qty
		if remaining > pool[key] {
			remaining = pool[key]
		}
		out = append(out, refundLineView{
			Index: i, Name: l.Name, SKU: l.SKU, UnitPrice: l.UnitPrice,
			Sold: l.Qty, Remaining: remaining, OrderType: l.OrderType,
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
			// ut-docs#944 (ut-docs#924 increment 2 of 4): a genuine DB-layer
			// failure, not a reachable business rejection -- same defect class
			// as #921/#923/#929/#316 elsewhere in this package.
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "refund.error.server", "refund", err) // page-error:allow not yet migrated, tracked in ut-docs#1458
			return
		}
		methods := []string{"cash"}
		for _, p := range detail.Payments {
			if p.Method != "cash" {
				methods = append(methods, p.Method)
			}
		}
		// German TSE hard gate (ADR-0048, ut-docs#1001): the refund path is
		// gated the same way the sale path is (enforceFiscalGate in the POST
		// handler below), but until now only the sale screen carried the
		// persistent override-active banner -- a cashier processing a refund
		// under an active override got no warning until they submitted the
		// form. Same read-only pattern as index_page.go: a gate read error
		// just renders no banner, the real gate still runs on submit.
		fiscalOverrideActive := false
		fiscalOverrideUntil := ""
		if g, gErr := evaluateFiscalGate(r.Context(), d); gErr == nil && g.Decision == fiscal.AllowedWithOverride {
			fiscalOverrideActive = true
			fiscalOverrideUntil = g.OverrideUntil.Local().Format("2006-01-02 15:04")
		}
		httpx.Render("ui/pages/refund.html", map[string]any{
			"title":                "Refund",
			"theme":                d.CurrentState().Theme,
			"menuItems":            d.MenuSnapshot(),
			"Sale":                 detail,
			"Lines":                refundableLines(detail, returned),
			"Methods":              methods,
			"AuthOff":              authOff,
			"fiscalOverrideActive": fiscalOverrideActive,
			"fiscalOverrideUntil":  fiscalOverrideUntil,
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

		// German TSE hard gate (ADR-0048, ut-docs#731): a refund moves real
		// money and is aufzeichnungspflichtig under KassenSichV the same as
		// a sale, so it's blocked the same way — checked before any
		// state-changing work (including the payment-provider refund
		// webhook below, which can itself move real money at the
		// provider). gate.Decision is inspected again after the return
		// completes, to write the same AllowedWithOverride audit marker
		// completeTender writes for a sale.
		gate, err := enforceFiscalGate(r.Context(), d)
		if err != nil {
			// Only the two gate sentinels are a 409 "the till refuses this
			// refund" — enforceFiscalGate also surfaces a settings-store
			// READ failure from EvaluateGate, which is an internal fault,
			// not a fiscal posture. Reporting that as
			// "no TSE is configured" would send the operator off to buy a
			// TSE they may already have. Still fails closed (no refund),
			// just with the honest status and copy.
			var fiscalNC *fiscalNeverConfiguredError
			var fiscalTF *fiscalTSEFailingError
			switch {
			case errors.As(err, &fiscalTF):
				common.LogAndLocalizedError(w, r, http.StatusConflict, "refund.error.fiscal_tse_failing", "refund", err)
			case errors.As(err, &fiscalNC):
				common.LogAndLocalizedError(w, r, http.StatusConflict, "refund.error.fiscal_never_configured", "refund", err)
			default:
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "refund.error.server", "refund", err)
			}
			return
		}

		returned, err := repo.ReturnedQuantities(r.Context(), detail.ID)
		if err != nil {
			// Same underlying call and failure class as the GET handler above.
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "refund.error.server", "refund", err)
			return
		}
		// Double-refund guard for the service charge itself (ut-docs#1215
		// review finding B1): how much of the ORIGINAL charge prior
		// completed returns already paid back, so THIS request's own
		// proration (below) can be clamped to never push the cumulative
		// total past the original charge -- same failure class as the
		// per-line quantity guard `returned` feeds above, just for the
		// charge instead of the lines.
		alreadyRefundedCharge, err := repo.RefundedServiceChargeTotal(r.Context(), detail.ID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "refund.error.server", "refund", err)
			return
		}
		locID, err := repo.EnsureStockLocation(r.Context())
		if err != nil {
			// Same generic internal-DB-failure class as the ReturnedQuantities
			// call above, so it gets the same refund-flow key. Deliberately
			// NOT pos_api.go's pos.toast.tender_failed ("SALE could not be
			// completed..."): the identical repo method is being called, but
			// the operator here pressed Refund, not Tender -- "sale" is the
			// wrong noun for what they did, and it would contradict the
			// neutral copy this same handler shows two calls earlier.
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "refund.error.server", "refund", err)
			return
		}

		// Needed below for the service-charge proration basis (net, not
		// gross -- ut-docs#1215) as well as computeRefundTotal further
		// down; hoisted here since it only depends on detail, not on which
		// lines end up refunded.
		inclusive := saleIsTaxInclusive(detail)

		// Collect requested quantities; enforce the double-refund guard.
		var lines []pos.SaleLineInput
		var refundGross, origGross int64
		var refundNetWeight int64
		// origNetWeight is the SAME true (tax-exclusive), net-after-line-
		// discount basis ApportionServiceChargeTax itself weighs bands by
		// (pos.TrueNetWeight) -- summed across every ORIGINAL line, not just
		// the ones this request refunds. Used below to prorate the service
		// charge by net share instead of gross share (ut-docs#1215).
		var origNetWeight int64
		for _, l := range detail.Lines {
			origGross += int64(float64(l.UnitPrice) * l.Qty)
			net := pos.AmountForQuantity(money.FromMinor(l.UnitPrice), l.Qty).Sub(money.FromMinor(l.LineDiscount))
			origNetWeight += pos.TrueNetWeight(net, l.TaxRateBP, inclusive).Minor()
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
			key := data.RefundLineKey(l.ItemID, l.VariantID, l.UnitPrice, l.OrderType)
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
				// ADR-0073 Decision 6: the return line keeps the original
				// line's mode; TaxRateBP above stays the money authority.
				OrderType: l.OrderType,
			})
			refundGross += int64(float64(l.UnitPrice) * qty)
			// Same true-net basis as origNetWeight above, this time over just
			// the lines THIS request refunds (ut-docs#1215).
			refundNet := pos.AmountForQuantity(money.FromMinor(l.UnitPrice), qty).Sub(money.FromMinor(lineDiscount))
			refundNetWeight += pos.TrueNetWeight(refundNet, l.TaxRateBP, inclusive).Minor()
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
		// Service charge (ut-docs#243, refined ut-docs#1215): prorated by
		// NET-AFTER-DISCOUNT -- the same true, tax-exclusive weighting basis
		// ApportionServiceChargeTax itself uses (ADR-0061 Decision 2), NOT
		// by gross. Gross-basis proration (the original #243 fix, matching
		// SaleDiscount's own gross proration one line up -- a deliberately
		// different basis, see N4 in
		// docs/code-reviews/2026-08-28-service-charge-refund-proration-243.md)
		// is exact for a single full refund but drifts on a SPLIT/partial
		// refund of a sale that mixes per-line discounts and different tax
		// rates alongside a charge apportioned across bands (not a flat
		// basis): the tax this amount feeds below (pos.ServiceChargeTax,
		// and pos.CompleteSale's own computeSaleTotals for the persisted
		// return) weighs by NET, so a gross-derived amount mixes two
		// different bases. Net-after-discount throughout matches that
		// downstream weighting for the common case (see ut-docs#1215's own
		// derivation for exactly which split-refund shapes it fixes and
		// which it doesn't -- it is NOT a general exactness guarantee, only
		// a closer approximation than gross was); a per-line-discounted,
		// multi-request split can still drift by a minor unit in EITHER
		// direction, which is exactly why the clamp below exists.
		var serviceChargeRefund int64
		if detail.ServiceCharge > 0 {
			switch {
			case origNetWeight > 0 && refundNetWeight > 0:
				serviceChargeRefund = detail.ServiceCharge * refundNetWeight / origNetWeight
			case origGross > 0:
				// Review findings B2 (round 1) + B3 (round 2): the net
				// basis can't apportion anything either when the WHOLE
				// sale's net-after-discount is zero (origNetWeight == 0,
				// B2's shape: every line fully line-discounted) OR when
				// only THIS REQUEST's own refunded lines have zero net
				// while other, unrefunded lines in the sale carry the
				// sale's net (refundNetWeight == 0, B3's shape: refunding
				// a comped/BOGO/staff-freebie line on its own, gross > 0
				// but net == 0, while a different line elsewhere in the
				// same sale is what makes origNetWeight positive). Either
				// way, fall back to the gross fraction (this card's
				// pre-fix basis, and the same edge
				// ApportionServiceChargeTax's own zero-weight rule exists
				// to handle on the sale side) so the sale stays refundable
				// instead of computing a $0 charge refund that then fails
				// CompleteSale's payment-must-be-positive check. Safe
				// against B1 either way: the clamp below applies
				// regardless of which branch produced the raw figure.
				serviceChargeRefund = detail.ServiceCharge * refundGross / origGross
			}
			// Review finding B1: clamp against what's ACTUALLY left to
			// refund, not just this request's own fraction of the whole.
			// Flooring the per-request prorated line discount above (line
			// ~277) makes each request's own refundNetWeight/refundGross
			// slightly larger than its true proportional share, so the SUM
			// of independently-computed per-request figures across several
			// sequential partial refunds of the same sale can exceed the
			// original charge -- a real money over-refund, verified via a
			// driven repro during review. This clamp is what actually
			// guarantees the invariant TestPostRefund_
			// UnevenSequentialRefundsNeverExceedTheOriginalServiceCharge
			// pins ("never more"): it no longer holds by luck of the
			// arithmetic, it's now enforced.
			if remaining := detail.ServiceCharge - alreadyRefundedCharge; serviceChargeRefund > remaining {
				if remaining < 0 {
					remaining = 0
				}
				serviceChargeRefund = remaining
			}
		}

		method := strings.TrimSpace(r.Form.Get("method"))
		if method == "" {
			method = "cash"
		}
		if err := repo.EnsurePaymentMethod(r.Context(), method); err != nil {
			// Same reasoning as EnsureStockLocation above: generic internal DB
			// failure on the refund path, so the refund-flow key, not
			// pos_api.go's sale-worded pos.toast.tender_failed.
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "refund.error.server", "refund", err)
			return
		}

		// Engine computes the refund total from the same inputs as the
		// original sale; the payment must cover it exactly. (`inclusive`
		// was already resolved above, before the service-charge proration.)
		refundTotal := computeRefundTotal(lines, money.FromMinor(saleDiscount), money.FromMinor(serviceChargeRefund), detail.ServiceChargeTaxBasisBP, inclusive)

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
			// ut-docs#950: `blocked` is a plugin-originated error -- whatever
			// text a third-party payment plugin's payment.<key>.refund hook
			// returned -- so it must never reach the operator verbatim, same
			// policy ut-docs#921 (F2) already established for the sibling
			// payment.<key>.authorize gate in pos_api.go's completeTender
			// (paymentDeclinedError / pos.toast.payment_declined): log the
			// real detail server-side, show a generic localized decline
			// message instead.
			common.LogAndLocalizedError(w, r, http.StatusPaymentRequired, "refund.error.provider_declined", "refund", blocked)
			return
		}
		saleInput := pos.SaleInput{
			SaleType:                "return",
			CashierID:               actorID,
			ActorID:                 actorID,
			Currency:                detail.Currency,
			TaxInclusive:            inclusive,
			SaleDiscount:            money.FromMinor(saleDiscount),
			ServiceCharge:           money.FromMinor(serviceChargeRefund),
			ServiceChargeTaxBasisBP: detail.ServiceChargeTaxBasisBP,
			Lines:                   lines,
			Payments:                []pos.PaymentInput{{MethodID: method, Amount: refundTotal, Currency: detail.Currency}},
			OriginalSaleID:          detail.ID,
			Note:                    "refund of " + detail.ReceiptNo,
			AllowNegativeInventory:  true, // returns only add stock back
			// ut-docs#1493: mirrors completeTender's (pos_api.go) own
			// offline-flag handling — the till's navigator.onLine-derived
			// offline state, threaded from #offline-flag via the "offline"
			// form field (review finding: unlike index.html, this page
			// carries no #offline-override manual-toggle checkbox, so —
			// deliberately, per this card's non-goals — that toggle does
			// NOT reach this signal; navigator.onLine alone drives it
			// here), so a known-offline refund also gets ADR-0044 D1's
			// known-offline short-circuit instead of burning the full 3s
			// fiscalSignAskBudget on a cloud call already known to fail.
			Offline: formFlagTruthy(r.Form.Get("offline")),
		}

		// fiscal.sign.ask (ADR-0044 Decision 1, ut-docs#999/#1405): a refund
		// moves real money and is aufzeichnungspflichtig under KassenSichV
		// exactly like a sale (the same reasoning the ADR-0048 gate above
		// already applies), so it gets a real signing attempt too — not just
		// the gate check. Dispatched here, after the payment-provider refund
		// webhook above has resolved and saleInput is final, mirroring
		// completeTender's own ordering (pos_api.go) exactly: after any
		// payment-provider interaction, before CompleteSale persists. Never
		// blocks or refuses the refund — any failure lands on the
		// proceed-and-declare surface below, same as a sale's.
		// saleInput.SaleType is already "return" (set above), so
		// buildFiscalSignPayload's SaleType field (contract 1.6.0,
		// ut-docs#1203) lets a signer tell this apart from a sale of the
		// same amount — the exact gap that blocked the first attempt at
		// this dispatch (universal-till PR #594, closed unmerged; see
		// docs/code-reviews/2026-09-03-fiscal-sign-refund-return-dispatch-1405.md).
		signRes := dispatchFiscalSignAsk(r.Context(), d, &saleInput)

		saleID, err := pos.CompleteSale(r.Context(), d.Db, saleInput)
		if err != nil {
			// Same underlying call pos_api.go's completeTender wraps --
			// classifyTenderError already maps its failure modes (insufficient
			// stock, underpayment, generic) to a localized key; reuse it
			// rather than reinventing the classification for a refund-created
			// "return" sale going through the identical CompleteSale path.
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, classifyTenderError(err), "refund", err)
			return
		}
		// Same per-completion audit marker completeTender writes for a sale
		// taken during an active TSE-override window (ADR-0048 Decision 3)
		// — the journal must show exactly which money-moving completions
		// (sale AND refund alike) were taken unsigned. Best-effort after
		// the fact, same as completeTender's own: the return is already
		// committed, so a failed marker write is logged, never unwinds it.
		if gate.Decision == fiscal.AllowedWithOverride {
			if auditErr := repo.InsertAudit(r.Context(), nil, actorID, "sale", saleID, "unsigned_override", map[string]any{
				"override_actor":  gate.OverrideActor,
				"override_reason": gate.OverrideReason,
				"override_until":  gate.OverrideUntil.UTC().Format(time.RFC3339),
			}, time.Now().UTC().Format(time.RFC3339), ""); auditErr != nil {
				log.Printf("fiscal gate: unsigned_override audit marker for refund %s failed: %v", saleID, auditErr)
			}
		}
		// fiscal.sign.ask proceed-and-declare (ADR-0044/ADR-0041 Decision E,
		// ut-docs#999/#1405): the refund is already committed — a failed (or
		// known-offline-skipped) signing dispatch is now DECLARED, never
		// unwound, exactly the way completeTender declares a sale's own
		// signing gap. Never re-attempted for a completed refund (ADR-0056,
		// ut-docs#839). Best-effort, log-only on failure, same as the
		// unsigned_override block above.
		if signRes.Outcome.isFailure() || signRes.Outcome == fiscalSignSkippedOffline {
			declareUnsignedFiscalSale(r.Context(), repo, saleID, actorID, signRes)
		}
		// ut-docs#585 (contract v1.1.0): an approved answer that carried the
		// §6 KassenSichV evidence gets it persisted against the refund's own
		// sale row, same as completeTender does for a sale. Evidence is only
		// ever non-nil on approved.
		if signRes.Outcome == fiscalSignApproved {
			recordFiscalTSEEvidence(r.Context(), repo, saleID, actorID, signRes.Evidence)
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
// covers the return exactly (CompleteSale enforces coverage). serviceCharge
// is the (already-prorated, ut-docs#243) share of the original sale's
// service charge this refund is returning, and chargeTaxBasisBP is the
// original sale's own basis (0 = apportion at the sale's own per-line
// rates) — both threaded straight from data.SaleDetail, same shape as
// saleDiscount above. Ordering mirrors pos.CompleteSale's own
// computeSaleTotals exactly (internal/pos/sales.go): the discount reduces
// subtotal BEFORE the charge is added (a sale discount never eats into the
// service charge), and the charge's own tax is folded into `tax` the same
// way a line's tax is -- exclusive adds it on top, inclusive keeps it
// embedded in the charge amount already counted in `total`.
func computeRefundTotal(lines []pos.SaleLineInput, saleDiscount, serviceCharge money.Money, chargeTaxBasisBP int, inclusive bool) money.Money {
	var subtotal, tax money.Money
	for _, l := range lines {
		net := pos.AmountForQuantity(l.UnitPrice, l.Qty).Sub(l.LineDiscount)
		t, _ := pos.ComputeTaxBasisPoints(net, l.TaxRateBasisPoints, inclusive)
		subtotal = subtotal.Add(net)
		tax = tax.Add(t)
	}
	tax = tax.Add(pos.ServiceChargeTax(serviceCharge, pos.ChargeTaxLinesFromSale(lines), inclusive, chargeTaxBasisBP))
	total := subtotal.Sub(saleDiscount).Add(serviceCharge)
	if !inclusive {
		total = total.Add(tax)
	}
	if total.IsNegative() {
		return 0
	}
	return total
}
