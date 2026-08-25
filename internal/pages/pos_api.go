package pages

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
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

// fiscalNeverConfiguredError signals the ADR-0048 hard block: a shop in a
// gated market (Germany) declared itself system-of-record without a TSE
// configured. There is deliberately NO override path for this state — same
// error shape as paymentDeclinedError so each tender surface maps it at its
// own call site, with no sale row created.
type fiscalNeverConfiguredError struct{}

func (e *fiscalNeverConfiguredError) Error() string {
	return "fiscal gate: shop is system of record but no TSE is configured"
}

// fiscalTSEFailingError signals the ADR-0048 configured-but-failing block:
// the shop's TSE is known-failing and no owner override window is currently
// active. Unlike fiscalNeverConfiguredError, an admin can lift this via
// POST /api/fiscal/tse-override (fiscal_api.go).
type fiscalTSEFailingError struct{}

func (e *fiscalTSEFailingError) Error() string {
	return "fiscal gate: TSE is failing and no owner override is active"
}

// fiscalSettingsReader resolves the settings source the gate reads —
// d.Settings when wired (production), a repo over d.Db otherwise (bare-Deps
// tests). Never reads d.Engine/d.KioskEngine (ADR-0020 kiosk isolation).
func fiscalSettingsReader(d *common.Deps) fiscal.SettingsReader {
	if d.Settings != nil {
		return d.Settings
	}
	return data.NewSettingsRepo(d.Db)
}

// formFlagTruthy reports whether a form checkbox/flag value spells "true"
// ("1"/"true"/"yes"/"on", case-insensitive) — the till's convention for the
// offline flag the cashier tender path and the self-order kiosk checkout
// both thread into SaleInput.Offline.
func formFlagTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// evaluateFiscalGate runs ADR-0048's policy for the shop's configured
// country against wall-clock now. For any non-gated country (everything but
// DE today) it returns Allowed without touching the settings store.
func evaluateFiscalGate(ctx context.Context, d *common.Deps) (fiscal.Gate, error) {
	return fiscal.EvaluateGate(ctx, fiscalSettingsReader(d), d.CurrentState().Country, time.Now())
}

// enforceFiscalGate evaluates the ADR-0048 German TSE hard gate and turns a
// blocking decision into the sentinel errors callers already switch on
// (fiscalNeverConfiguredError / fiscalTSEFailingError). Shared by every
// money-moving completion path, not just completeTender's own sale/kiosk
// tender: a refund moves real money and is aufzeichnungspflichtig under
// KassenSichV the same as a sale (ut-docs#731, decided 2026-08-18), so the
// refund page and CreateReturn route through this exact check rather than a
// separately maintained copy that could silently drift from it. The
// returned Gate is valid even on a nil error so a caller can still inspect
// gate.Decision == fiscal.AllowedWithOverride to write its own per-completion
// audit marker, mirroring completeTender's own unsigned_override marker
// below.
func enforceFiscalGate(ctx context.Context, d *common.Deps) (fiscal.Gate, error) {
	gate, err := evaluateFiscalGate(ctx, d)
	if err != nil {
		return gate, err
	}
	switch gate.Decision {
	case fiscal.BlockedNeverConfigured:
		// Hard block, unconditionally — no override path exists for this
		// branch, by design (ADR-0048 Decision 2.2).
		return gate, &fiscalNeverConfiguredError{}
	case fiscal.BlockedTSEFailing:
		return gate, &fiscalTSEFailingError{}
	}
	return gate, nil
}

// completeTender runs the money-critical authorize -> complete -> publish
// pipeline shared by every till-mode's tender path (cashier and self-order
// kiosk alike): payment authorization gate, CompleteSale, basket reset via
// `engine` (the basket the tendered sale was rung on), plugin trigger_event
// fan-out, ERP/inventory mirroring, and silent receipt/kitchen printing. It
// must behave identically regardless of the caller, so it lives in exactly
// one place rather than being duplicated per surface. actorID is attributed
// on printed receipts/tickets. The cashier tender path passes d.Engine; the
// kiosk checkout passes d.KioskEngine (ut-docs#449: an anonymous kiosk
// checkout must never reset the cashier's live basket, and vice versa).
func completeTender(ctx context.Context, d *common.Deps, engine *pos.Service, repo *data.POSRepo, saleInput pos.SaleInput, payments []pos.PaymentInput, actorID string) (string, error) {
	// German TSE hard gate (ADR-0048, ut-docs#715) — evaluated BEFORE the
	// payment.<key>.authorize loop: "never configured" needs no plugin
	// round trip, just local settings reads (never the network — a till
	// that is merely offline is NOT a failing TSE, and checkout must never
	// block on connectivity, ADR-0003). Expiry of an override window is
	// enforced right here by re-evaluating against wall-clock time on every
	// tender — no background job; blocking resumes on the next attempt.
	gate, err := enforceFiscalGate(ctx, d)
	if err != nil {
		return "", err
	}

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
	// Tip recipient default (ADR-0061 Decision 3), decided HERE — the one
	// choke point every tender surface (cashier and kiosk) goes through,
	// right where a plugin-reported tip also lands: an installed country
	// plugin's charge.policy.ask answer supplies the market default
	// (tip_default_recipient); with no answer, "employee" — the one default
	// every researched market agrees on. Only fills payments that carry no
	// explicit recipient; pos.CompleteSale re-validates and re-defaults at
	// persistence as the backstop.
	tipRecipient := pos.TipRecipientEmployee
	if policy, ok := engine.ChargePolicy(); ok && policy.TipDefaultRecipient == pos.TipRecipientBusiness {
		tipRecipient = pos.TipRecipientBusiness
	}
	for i := range payments {
		if payments[i].TipRecipient == "" {
			payments[i].TipRecipient = tipRecipient
		}
	}
	// Both call sites happen to pass a `payments` slice that shares
	// `saleInput.Payments`'s backing array, so the mutation above already
	// reaches CompleteSale by aliasing — but relying on that silently is
	// fragile (a caller passing a defensive copy would drop every
	// plugin-reported tip with no compile error and no test failure).
	// Make it explicit instead.
	saleInput.Payments = payments

	// fiscal.sign.ask (ADR-0044 Decision 1, ut-docs#675): the tender-phase
	// fiscal signing point fires HERE — after payment.<key>.authorize has
	// resolved (the payable total, including any reader-reported tip above,
	// is final) and before CompleteSale persists the sale. This ordering is
	// load-bearing: signing concurrently with an in-flight authorize could
	// produce an irreversible TSE record for a sale that is then declined
	// and never persisted. Orthogonal to the ADR-0048 hard gate at the top
	// of this function — the gate decides whether a sale may START; this
	// point signs (or declares) every sale that actually completes, and
	// fires regardless of the gate's decision. Never blocks or refuses the
	// sale: any failure lands on the proceed-and-declare surface below.
	signRes := dispatchFiscalSignAsk(ctx, d, &saleInput)

	saleID, err := pos.CompleteSale(ctx, d.Db, saleInput)
	if err != nil {
		return "", err
	}

	// Every sale completed during an active TSE-override window gets its
	// own audit marker (entity sale, action unsigned_override) — a per-sale
	// flag distinct from the one-time fiscal_override/grant entry, so the
	// journal shows exactly which sales were taken unsigned (ADR-0048
	// Decision 3). Best-effort after the fact: the sale itself is already
	// committed, so a failed marker write is logged, never unwinds a sale.
	if gate.Decision == fiscal.AllowedWithOverride {
		if auditErr := repo.InsertAudit(ctx, nil, actorID, "sale", saleID, "unsigned_override", map[string]any{
			"override_actor":  gate.OverrideActor,
			"override_reason": gate.OverrideReason,
			"override_until":  gate.OverrideUntil.UTC().Format(time.RFC3339),
		}, time.Now().UTC().Format(time.RFC3339), ""); auditErr != nil {
			log.Printf("fiscal gate: unsigned_override audit marker for sale %s failed: %v", saleID, auditErr)
		}
	}

	// fiscal.sign.ask proceed-and-declare (ADR-0044/ADR-0041 Decision E):
	// the sale is already committed — a failed (or known-offline-skipped)
	// signing dispatch is now DECLARED, never unwound: journal marker,
	// receipt outage notice (derived from that marker by both render
	// paths), operator Problem. The declaration is permanent — signing is
	// never re-attempted for a completed sale (ADR-0056, ut-docs#839).
	// Best-effort, log-only on failure, exactly like the unsigned_override
	// block above.
	if signRes.Outcome.isFailure() || signRes.Outcome == fiscalSignSkippedOffline {
		declareUnsignedFiscalSale(ctx, repo, saleID, actorID, signRes)
	}
	// ut-docs#585 (contract v1.1.0): an approved answer that carried the §6
	// KassenSichV evidence gets it persisted against the sale so both
	// receipt render paths can show it. Best-effort, log-only on failure,
	// exactly like the two declare blocks above — never unwind a committed
	// sale over bookkeeping. Evidence is only ever non-nil on approved; a
	// bare approval (or a pre-1.1.0 signer) is a no-op here.
	if signRes.Outcome == fiscalSignApproved {
		recordFiscalTSEEvidence(ctx, repo, saleID, actorID, signRes.Evidence)
	}

	engine.Reset()

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

// classifyTenderError maps a completeTender/CompleteSale failure (declined
// payment and the fiscal hard gate are handled separately as typed errors
// before this is ever consulted — see completeTender's callers) to the
// locale key for its cashier-facing toast. Everything CompleteSale can
// still fail with is a plain error, not a typed one (insufficient stock,
// an underpayment, a lines/DB failure), so this matches on the message
// text the same way the pre-#921 insufficient-stock branch already did.
// Anything the switch doesn't specifically recognize gets the generic
// fallback key — mirroring the self-order kiosk tender handler's own
// default (self_order_shop.go's "selforder.checkout.failed") — so a
// failure mode nobody has named yet still renders as a localized message,
// never raw Go error text (ut-docs#921).
func classifyTenderError(err error) string {
	switch {
	case strings.Contains(err.Error(), "insufficient stock"):
		return "pos.toast.insufficient_stock"
	case strings.Contains(err.Error(), "do not cover total"):
		return "pos.toast.payment_insufficient"
	// Tracked voucher redemption failures (ut-docs#1008): matched with
	// errors.Is against the exported sentinels, not by message substring
	// (review minor F9) — CompleteSale returns the repo's wrapped error
	// unstringified (db.WithTx and completeTender both pass it through), so
	// the sentinel survives to here and a rewording of the error text can
	// no longer silently break the classification.
	case errors.Is(err, data.ErrVoucherInsufficientBalance):
		return "pos.toast.voucher_insufficient"
	case errors.Is(err, data.ErrVoucherNotFound), errors.Is(err, data.ErrVoucherNotActive):
		return "pos.toast.voucher_invalid"
	case errors.Is(err, pos.ErrVoucherOvertender):
		return "pos.toast.voucher_overtender"
	default:
		return "pos.toast.tender_failed"
	}
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

	// Table assignment (ut-docs#820, ADR-0054): assign the current basket to
	// a dining table, or clear the assignment when table_id is empty. The
	// label is resolved SERVER-SIDE from the tables repo, never trusted from
	// the request -- the picker only ever POSTs an id, and an id that
	// doesn't resolve (deleted/garbage) degrades to "no table assigned"
	// rather than stamping a stale/bogus label onto the basket, same
	// fail-safe shape as order-type's own clamp-to-known-values above.
	mux.HandleFunc("/api/pos/table", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		tableID := strings.TrimSpace(r.Form.Get("table_id"))
		var b *pos.Basket
		if tableID == "" {
			b = d.Engine.ClearTable()
		} else if tbl, ok, err := data.NewPOSRepo(d.Db).GetTable(r.Context(), tableID); err == nil && ok {
			b = d.Engine.SetTable(tableID, tbl.Label)
		} else {
			b = d.Engine.ClearTable()
		}
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
				// VoucherID (ut-docs#1008) marks this payment as a TRACKED
				// voucher redemption against a real vouchers.id — method must
				// be "voucher", and the balance must cover the amount (a
				// shortfall rejects the tender). Empty keeps the generic,
				// untracked voucher payment exactly as before.
				VoucherID string `json:"voucher_id,omitempty"`
			} `json:"payments"`
			// IssueVouchers (ut-docs#1008) sells multi-purpose vouchers in
			// this sale: each becomes a 0% liability (vouchers +
			// voucher_transactions rows), excluded from article revenue and
			// VAT bands, included in the amount the customer owes. Code is
			// the optional preprinted voucher identifier (generated when
			// empty); HolderLabel an optional free-text holder name.
			// Currently API-only — no cashier dialog builds this yet (the
			// card's accepted minimal entry point; UI pass is a follow-up).
			IssueVouchers []struct {
				Amount      int64  `json:"amount"`
				Code        string `json:"code,omitempty"`
				HolderLabel string `json:"holder_label,omitempty"`
			} `json:"issue_vouchers,omitempty"`
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
				if formFlagTruthy(r.Form.Get("offline_override")) {
					offline = true
					offlineSet = true
				}
				if !offlineSet && formFlagTruthy(r.Form.Get("offline")) {
					offline = true
					offlineSet = true
				}
			}
		}

		lines := d.Engine.Lines()
		// A voucher-only sale (ut-docs#1008) legitimately rings no article
		// line — a customer buying just a gift voucher.
		if len(lines) == 0 && len(in.IssueVouchers) == 0 {
			http.Error(w, "no items in basket", http.StatusBadRequest)
			return
		}

		// Validate voucher-issue input up front (validate all external
		// input): a non-positive or absurdly large amount (the
		// pos.MaxVoucherIssueAmount sanity ceiling — review F3: unbounded
		// amounts near 2^62 overflowed the int64 total negative and slipped
		// past the payment-coverage check) or an oversized code/label is a
		// caller bug, refused before any DB work.
		var voucherIssues []pos.VoucherIssueInput
		var voucherIssueTotal money.Money
		for _, v := range in.IssueVouchers {
			if v.Amount <= 0 || money.FromMinor(v.Amount) > pos.MaxVoucherIssueAmount || len(v.Code) > 64 || len(v.HolderLabel) > 200 {
				http.Error(w, "invalid voucher issue", http.StatusBadRequest)
				return
			}
			voucherIssues = append(voucherIssues, pos.VoucherIssueInput{
				VoucherID:   v.Code,
				HolderLabel: v.HolderLabel,
				Amount:      money.FromMinor(v.Amount),
			})
			voucherIssueTotal = voucherIssueTotal.Add(money.FromMinor(v.Amount))
		}

		locID, err := repo.EnsureStockLocation(r.Context())
		if err != nil {
			// ut-docs#929: same defect class as ut-docs#921/#923 in this same
			// handler -- a genuine internal/DB-layer failure (the
			// stock_locations upsert itself), not a reachable business
			// rejection, so the 500 status is right but the body must not be
			// raw Go error text. Reuses the same generic "ask an
			// administrator" copy for the same reason #923's fix does.
			log.Printf("tender rejected: ensure stock location: %v", err)
			http.Error(w, httpx.T(httpx.ResolveLocale(w, r), "pos.toast.tender_failed"), http.StatusInternalServerError)
			return
		}

		var saleLines []pos.SaleLineInput
		var blockedLines []pos.BasketLine
		for _, l := range lines {
			// Single source of truth with the live basket preview
			// (Service.recomputeTotals) — same order-type-aware resolution,
			// so what the cashier saw pre-payment is what gets recorded.
			taxBP, taxBlocked := d.Engine.EffectiveLineTaxRateBP(l)
			if taxBlocked {
				// ut-docs#368 — fail closed on tax: this line's registered
				// tax plugin is broken right now (install_state='broken'),
				// so its true rate is unknowable; recording it at the base
				// rate would be silently-wrong tax, not a degraded sale.
				// Collect ALL blocked lines before answering — how many are
				// blocked decides what the honest message is (below).
				blockedLines = append(blockedLines, l)
				continue
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
				Modifiers:          l.Modifiers,
			})
		}
		if len(blockedLines) > 0 {
			// The block is only per-line when exactly ONE line is blocked:
			// the asker can't know which lines a broken plugin WOULD have
			// answered for (any tax.rate.ask subscriber may answer any
			// line), so when the till's only tax authority is broken EVERY
			// line blocks — and "remove the item" would just move the same
			// block to the next item (round-2 review of ut-docs#368: the
			// message must not overclaim a granularity the architecture
			// can't deliver). One blocked line → name it, removing it really
			// does let the rest complete; several → say checkout is
			// unavailable until the plugin recovers (self-heal ~30s tick for
			// marketplace installs). Same toast shape as the
			// insufficient-stock rejection below — never a modal blocker
			// (offline-first UI rules).
			locale := httpx.ResolveLocale(w, r)
			funcs := httpx.FuncsFor(locale)
			msg := httpx.T(locale, "pos.toast.tax_unavailable_all")
			if len(blockedLines) == 1 {
				msg = fmt.Sprintf(httpx.T(locale, "pos.toast.tax_unavailable"), blockedLines[0].Name)
			}
			log.Printf("tender rejected: tax authority broken for %d of %d basket lines, first %q (tax code %q) (ut-docs#368 fail-closed)",
				len(blockedLines), len(lines), blockedLines[0].Name, blockedLines[0].TaxCodeID)
			b := d.Engine.Basket()
			b.ToastMessage = msg
			b.ToastLevel = "error"
			basketView, _ := ui.NewBasketView(funcs)
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			_ = basketView.Render(w, b)
			return
		}

		var payments []pos.PaymentInput
		for _, p := range in.Payments {
			if p.Method == "" || p.Amount <= 0 {
				continue
			}
			if err := repo.EnsurePaymentMethod(r.Context(), p.Method); err != nil {
				// ut-docs#923: a genuine internal/setup failure (the FK-upsert
				// itself hit a DB-layer error), not a reachable business
				// rejection -- the 500 status is right, but the body must not
				// be raw Go error text either (ut-docs#921's fix for the
				// business-rejection branches below). No dedicated classifier:
				// this call has exactly one failure mode worth naming (the
				// underlying DB op failed), so it reuses the same generic
				// "ask an administrator" copy classifyTenderError's own
				// default branch renders, mirroring pos.toast.payment_declined's
				// http.Error(w, httpx.T(...), status) shape above.
				log.Printf("tender rejected: ensure payment method %q: %v", p.Method, err)
				http.Error(w, httpx.T(httpx.ResolveLocale(w, r), "pos.toast.tender_failed"), http.StatusInternalServerError)
				return
			}
			// A redemption id gets the same bound the issue path's Code and
			// GET /api/vouchers/{id} already enforce (review minor F6) —
			// TrimSpace alone let through what every other voucher-id
			// surface rejects. Control characters are refused one layer
			// down by pos.CompleteSale's shared validateVoucherID.
			voucherID := strings.TrimSpace(p.VoucherID)
			if len(voucherID) > 64 {
				http.Error(w, "invalid voucher id", http.StatusBadRequest)
				return
			}
			payments = append(payments, pos.PaymentInput{
				MethodID:    p.Method,
				Amount:      money.FromMinor(p.Amount),
				Currency:    p.Currency,
				Reference:   p.Reference,
				ChangeGiven: money.FromMinor(p.Change),
				TipAmount:   money.FromMinor(p.Tip),
				VoucherID:   voucherID,
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
						// Same ut-docs#923 fix as the JSON-payments branch above --
						// form-encoded quick-tender buttons hit this identical
						// FK-upsert failure path and used to leak the same raw text.
						log.Printf("tender rejected: ensure payment method %q: %v", method, err)
						http.Error(w, httpx.T(httpx.ResolveLocale(w, r), "pos.toast.tender_failed"), http.StatusInternalServerError)
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
		serviceCharge, _ := pos.ComputeTaxBasisPoints(subtotal.Sub(discount), common.EffectiveServiceChargeRateBP(d.CurrentState()), false)
		// ADR-0061: an installed country plugin's charge.policy.ask answer
		// can forbid the charge outright or fix a flat tax basis for it; no
		// answer (the normal no-plugin case) leaves the fail-closed default —
		// the charge stays permitted and its tax is apportioned at the
		// sale's own per-line rates. Same consult the basket preview makes
		// (Service.recomputeTotals), so screen and demand agree. Runs AFTER
		// the ut-docs#962 Turkey backstop above, which may have already
		// zeroed serviceCharge to 0 — ServiceChargeTax(0, ...) is a no-op,
		// so the two mechanisms compose without either needing to know
		// about the other.
		chargeTaxBasisBP := 0
		if policy, ok := d.Engine.ChargePolicy(); ok {
			if !policy.ServiceChargePermitted {
				serviceCharge = 0
			}
			chargeTaxBasisBP = policy.ServiceChargeTaxBasisBP
		}
		chargeTax := pos.ServiceChargeTax(serviceCharge, pos.ChargeTaxLinesFromSale(saleLines), d.CurrentState().TaxInclusive, chargeTaxBasisBP)
		total := subtotal.Sub(discount).Add(serviceCharge)
		if !d.CurrentState().TaxInclusive {
			// Exclusive pricing: the charge's tax rides on top exactly like
			// each line's own (inclusive carries it inside the amount) —
			// mirrors pos.computeSaleTotals, which is what CompleteSale's
			// payment-sufficiency check will enforce below.
			total = total.Add(taxTotal).Add(chargeTax)
		}
		if total.IsNegative() {
			total = 0
		}
		// Voucher issues ride on top AFTER the clamp, mirroring
		// pos.computeSaleTotals exactly — the full face value is owed
		// regardless of any article discount (ut-docs#1008), so what's
		// demanded here and what CompleteSale enforces agree.
		total = total.Add(voucherIssueTotal)
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

		// Captured BEFORE completeTender (which resets the basket on
		// success) -- the receipt render below needs the table label the
		// sale was actually served at, same reasoning as capturing
		// discountType/discountRaw from the basket further down.
		tableLabelForReceipt := d.Engine.TableLabel()

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
				// ut-docs#944 (ut-docs#924 increment 2 of 4): the audit-record
				// write for a simulated payment failure is an internal/DB-layer
				// failure, not a reachable business rejection -- same defect
				// class as #921/#923/#929 above, just a different call site.
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pos.error.server", "pos-api", err)
				return
			}
			http.Error(w, "payment failed; retry required", http.StatusBadGateway)
			return
		}

		saleInput := pos.SaleInput{
			SaleType:      "sale",
			Currency:      d.CurrentState().Currency,
			TaxInclusive:  d.CurrentState().TaxInclusive,
			SaleDiscount:  discount,
			ServiceCharge: serviceCharge,
			// The plugin-answered flat basis (0 = per-line apportionment)
			// travels with the sale so computeSaleTotals and the
			// fiscal.sign.ask payload tax the charge identically (ADR-0061).
			ServiceChargeTaxBasisBP: chargeTaxBasisBP,
			OrderType:               d.Engine.OrderType(),
			TableID:                 d.Engine.TableID(),
			Lines:                   saleLines,
			Payments:                payments,
			Note:                    in.Note,
			RegisterID:              registerID,
			CashierID:               cashierID,
			CustomerID:              customerID,
			AllowNegativeInventory:  allowNegative,
			ActorID:                 cashierID,
			Offline:                 offline,
			VoucherIssues:           voucherIssues,
		}
		saleID, err := completeTender(r.Context(), d, d.Engine, repo, saleInput, payments, getSessionUserID(r))
		if err != nil {
			var declined *paymentDeclinedError
			if errors.As(err, &declined) {
				// ut-docs#921 review finding (F2): the same raw-English leak
				// applied here too -- err.Error() ("payment declined: card")
				// went straight to the operator regardless of locale. The
				// 402 status is deliberate (its own type comment: lets a
				// caller distinguish a decline from a generic 400) and
				// stays; only the body swaps from raw Go error text to a
				// localized, method-agnostic message, mirroring the
				// self-order kiosk's own "selforder.checkout.declined" copy.
				log.Printf("tender rejected: %v", err)
				http.Error(w, httpx.T(httpx.ResolveLocale(w, r), "pos.toast.payment_declined"), http.StatusPaymentRequired)
				return
			}
			// German TSE hard gate (ADR-0048): same in-place, localized
			// notice surface as the insufficient-stock rejection below —
			// never a modal blocker, the basket survives, no sale row was
			// created. The two block states get their own copy: never-
			// configured names the fix (get a TSE set up / leave system-of-
			// record mode), failing names the owner override.
			var fiscalNC *fiscalNeverConfiguredError
			var fiscalTF *fiscalTSEFailingError
			if errors.As(err, &fiscalNC) || errors.As(err, &fiscalTF) {
				msgKey := "pos.toast.fiscal_never_configured"
				if errors.As(err, &fiscalTF) {
					msgKey = "pos.toast.fiscal_tse_failing"
				}
				log.Printf("tender rejected: %v (ADR-0048 fiscal hard gate)", err)
				locale := httpx.ResolveLocale(w, r)
				funcs := httpx.FuncsFor(locale)
				b := d.Engine.Basket()
				b.ToastMessage = httpx.T(locale, msgKey)
				b.ToastLevel = "error"
				basketView, _ := ui.NewBasketView(funcs)
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				_ = basketView.Render(w, b)
				return
			}
			// Any other completeTender/CompleteSale failure — insufficient
			// stock, an underpayment, or anything not specifically
			// classified — never leaks the raw engine error text to the
			// operator (it used to, via http.Error(w, err.Error(), ...):
			// verbatim English regardless of locale, ut-docs#921). Same
			// in-place, localized notice surface as the fiscal-gate
			// rejection above; the raw detail stays available server-side
			// via the log below. classifyTenderError's default branch
			// mirrors the self-order kiosk handler's own fallback
			// ("selforder.checkout.failed") so nothing falls through to
			// raw English again, known cause or not.
			locale := httpx.ResolveLocale(w, r)
			funcs := httpx.FuncsFor(locale)
			b := d.Engine.Basket()
			log.Printf("tender rejected: %v", err)
			b.ToastMessage = httpx.T(locale, classifyTenderError(err))
			b.ToastLevel = "error"
			basketView, _ := ui.NewBasketView(funcs)
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			_ = basketView.Render(w, b)
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
		// Re-evaluate the fiscal gate for the receipt's unsigned-override
		// marker line (ADR-0048): the sale just completed, so an active
		// override window here means this sale was taken under it. A read
		// error degrades to "no marker line" — the authoritative per-sale
		// flag is the unsigned_override audit row completeTender wrote.
		unsignedOverride := false
		if g, gErr := evaluateFiscalGate(r.Context(), d); gErr == nil && g.Decision == fiscal.AllowedWithOverride {
			unsignedOverride = true
		}
		// fiscal.sign.ask outage/cannot-sign notice (ADR-0044
		// proceed-and-declare, ut-docs#835): derived from the sale's own
		// audit rows — NOT a gate/settings re-evaluation, since this is a
		// per-sale outcome, not current-settings state — and suppressed
		// only when a historical fiscal_signing_resolved row shows a
		// pre-1.4.0 build's background retry signed the sale back when
		// that mechanism existed (no current code writes that row —
		// ADR-0056, ut-docs#839). The two notices are mutually exclusive:
		// a sale carries at most one of the two audit actions.
		fiscalGapAction := saleFiscalSigningGapKind(r.Context(), repo, saleID)
		unsignedFiscalSigning := fiscalGapAction == fiscalSignGapActionSigning
		unsignedCannotSign := fiscalGapAction == fiscalSignGapActionCannotSign
		// ut-docs#585: the sale's recorded TSE evidence, if any — same
		// per-sale derivation as the two flags above (the sale's own
		// records, never current settings). A read error degrades to "no
		// block": the receipt must render regardless, and absence of
		// evidence is shown as absence, never placeholders.
		tseSignature, _, tseErr := repo.GetFiscalTSESignature(r.Context(), saleID)
		if tseErr != nil {
			tseSignature = nil
		}
		receiptHTML, renderErr := renderReceipt(funcs, receiptNo, saleLines, payments, dbSubtotal, dbTax, dbTotal, d.CurrentState().TaxInclusive, discount.Minor(), discountType, discountRaw, legalBlocks, printerUnavailable, unsignedOverride, unsignedFiscalSigning, unsignedCannotSign, tseSignature,
			storeNameOrDefault(r.Context(), d), receiptDesignFromSettings(r.Context(), d), tableLabelForReceipt)
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
			// ut-docs#944: raw Go error text (a wrapped SQL/engine error, or
			// ErrSaleNotFound's own "sale not found: <id>") used to reach the
			// operator verbatim regardless of locale. Not-found gets its own
			// key -- distinct status, distinct meaning -- everything else
			// (bad status value, DB failure) shares the generic fallback.
			if errors.Is(err, data.ErrSaleNotFound) {
				common.LogAndLocalizedError(w, r, http.StatusNotFound, "pos.error.sale_not_found", "pos-api", err)
				return
			}
			// Void refused because a voucher issued in this sale has already
			// been (partly) spent elsewhere (ut-docs#1008 review F2) — a
			// distinct, actionable refusal, not a server fault: the operator
			// needs to know WHY this specific void cannot happen.
			if errors.Is(err, data.ErrVoucherRedeemedCannotVoid) {
				common.LogAndLocalizedError(w, r, http.StatusConflict, "pos.error.void_voucher_redeemed", "pos-api", err)
				return
			}
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "pos.error.server", "pos-api", err)
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
	// MaskedPAN/AuthCode are the standard EC-receipt line (ut-docs#543) --
	// shown instead of Reference when a card-present payment supplied
	// them. Already-masked by the time it reaches here (pos.CompleteSale
	// validates this at persistence); never the full PAN.
	MaskedPAN string
	AuthCode  string
}

type receiptLegalBlock struct {
	PluginID      string
	PluginName    string
	PluginVersion string
	Priority      int
	Lines         []string
}

// receiptTSEView is the template's view of a sale's recorded §6 KassenSichV
// TSE evidence (ut-docs#585) — built in renderReceipt from the persisted
// fiscal_tse_signatures row, nil when the sale has none (the template then
// renders no TSE block at all, never placeholders).
type receiptTSEView struct {
	TransactionNumber  int64
	SignatureCounter   int64
	SerialNumber       string
	StartTime          string
	LogTime            string
	Signature          string
	SignatureAlgorithm string
	// QRDataURI is a data:image/png;base64 URI of the evidence QR code
	// (same inline-embed pattern as settings_page.go's claim QR); empty if
	// encoding failed, in which case only the text lines render.
	//
	// ut-docs#906: must be template.URL, not string. receipt.html renders
	// this via html/template (this file switched from text/template to
	// html/template in the same fix — text/template applied NO escaping
	// at all to any receipt field, not just this one). html/template's
	// contextual auto-escaper treats a plain string in an <img src="…">
	// position as an untrusted URL and strips any non-http(s)/mailto
	// scheme — including data: — down to the safe placeholder
	// "#ZgotmplZ". template.URL is the package's own signal that this
	// value was constructed here, not from user input, and is safe to
	// emit as-is.
	QRDataURI template.URL
}

// buildTSEQRPayload assembles the QR payload from the recorded evidence.
//
// PROVISIONAL FORMAT (ut-docs#585): a labeled, pipe-delimited string —
// "UT-TSE-V0|serial|transaction|counter|start|log|algorithm|signature".
// The exact byte-for-byte QR payload German receipt practice expects (the
// DSFinV-K/vendor TSE-QR-code convention) has NOT been verified against the
// authoritative spec or any real TSE vendor — no fiskaly sandbox/real TSE
// was available (the same constraint ut-docs#757 records). The format MUST
// be confirmed (and likely revised) against a real TSE before this ships to
// a live German shop; the "UT-TSE-V0" prefix marks the payload as ours and
// provisional rather than letting it masquerade as the official format.
func buildTSEQRPayload(sig *data.FiscalTSESignature) string {
	return strings.Join([]string{
		"UT-TSE-V0",
		sig.SerialNumber,
		strconv.FormatInt(sig.TransactionNumber, 10),
		strconv.FormatInt(sig.SignatureCounter, 10),
		sig.StartTime,
		sig.LogTime,
		sig.SignatureAlgorithm,
		sig.Signature,
	}, "|")
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

func renderReceipt(funcs template.FuncMap, receiptNo string, lines []pos.SaleLineInput, payments []pos.PaymentInput, subtotal, taxTotal, total int64, taxInclusive bool, saleDiscount int64, saleDiscountType string, saleDiscountRaw int64, legalBlocks []receiptLegalBlock, printerUnavailable bool, unsignedOverride bool, unsignedFiscalSigning bool, unsignedCannotSign bool, tseSignature *data.FiscalTSESignature, storeName string, design receiptDesign, tableLabel string) (string, error) {
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
			MaskedPAN: p.MaskedPAN,
			AuthCode:  p.AuthCode,
		})
	}
	// ut-docs#585: the sale's recorded §6 KassenSichV evidence, when any —
	// nil renders no block at all. The QR embed mirrors settings_page.go's
	// claim-QR pattern (qrcode.Encode → base64 PNG data URI); an encode
	// failure degrades to text lines only, never fails the receipt.
	var tseView *receiptTSEView
	if tseSignature != nil {
		tseView = &receiptTSEView{
			TransactionNumber:  tseSignature.TransactionNumber,
			SignatureCounter:   tseSignature.SignatureCounter,
			SerialNumber:       tseSignature.SerialNumber,
			StartTime:          tseSignature.StartTime,
			LogTime:            tseSignature.LogTime,
			Signature:          tseSignature.Signature,
			SignatureAlgorithm: tseSignature.SignatureAlgorithm,
		}
		if png, err := qrcode.Encode(buildTSEQRPayload(tseSignature), qrcode.Medium, 140); err == nil {
			tseView.QRDataURI = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
		}
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
		// ADR-0048: a sale completed during an active TSE-override window
		// carries a receipt line marking it as taken during a documented
		// TSE-failure window.
		"UnsignedOverride": unsignedOverride,
		// ADR-0044 proceed-and-declare: a sale whose fiscal.sign.ask
		// dispatch failed (or was skipped known-offline) carries a visible
		// outage notice — the gap must never look like a normal sale.
		"UnsignedFiscalSigning": unsignedFiscalSigning,
		// ut-docs#835: a sale whose signer explicitly declared it CANNOT be
		// signed as presented (a property of the sale's own data, not a
		// connectivity problem) gets its own notice, worded accordingly —
		// mutually exclusive with UnsignedFiscalSigning above.
		"UnsignedCannotSign": unsignedCannotSign,
		// ut-docs#585: recorded TSE signing evidence — nil means no block.
		"TSESignature": tseView,
		// Receipt design (docs: receipt-designer.md): the on-screen copy
		// follows the same owner-styled design as the thermal print.
		"StoreName": storeName,
		// TableLabel (ut-docs#820, ADR-0054): the dining table this sale was
		// served at, resolved server-side before the basket was reset — ""
		// when no table was assigned, guarded by {{ if .TableLabel }} in the
		// template.
		"TableLabel":   tableLabel,
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
			MaskedPAN:   p.MaskedPAN,
			AuthCode:    p.AuthCode,
			TerminalID:  p.TerminalID,
			TraceID:     p.TraceID,
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
