package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
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
	// HasVariants (ut-docs#2209) — see ui.Button.HasVariants: the kiosk
	// grid template ORs it with HasModifiers to decide whether tapping the
	// tile opens the picker or adds straight to the cart.
	HasVariants bool
	ImageURL    string
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
	hasVariants, variantsErr := repo.ItemIDsWithVariants(ctx, ids)
	if variantsErr != nil {
		// Same reasoning as ButtonStore.Load's own guard (ut-docs#2209
		// review, finding 5): on this error every kiosk tile reverts to
		// adding the PARENT base price, so it must never fail silently.
		logging.L().Warnf("kiosk: load items-with-variants failed, every tile falls back to parent-price add (ut-docs#2209): %v", variantsErr)
	}
	currentPrices, pricesErr := repo.ItemCurrentPrices(ctx, ids)
	if pricesErr != nil {
		// Same non-fatal-but-loud treatment as hasVariants above
		// (ut-docs#2258): on this error every kiosk tile falls back to the
		// STALE configured base_price it.BasePrice already carries below.
		logging.L().Warnf("kiosk: load item current prices failed, every tile falls back to raw base_price (ut-docs#2258): %v", pricesErr)
	}
	thumbnails, _ := repo.ItemThumbnails(ctx) // best-effort: a read error just means every tile falls back to no-image, same as a missing row

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
		// ut-docs#2258: prefer the batched price_history-aware price;
		// it.BasePrice is the fallback for an id ItemCurrentPrices didn't
		// return (lookup error, or the item row is gone) — never a silent
		// zero.
		price := it.BasePrice
		if p, ok := currentPrices[it.ID]; ok {
			price = p
		}
		out = append(out, shopItem{
			ItemID:       it.ID,
			Name:         it.Name,
			Description:  it.Description,
			CategoryID:   categoryID,
			Code:         code,
			PriceMinor:   price,
			HasModifiers: hasMods[it.ID],
			HasVariants:  hasVariants[it.ID],
			// item_images (ut-docs#1870), not a hardcoded upload-only path:
			// an item's thumbnail may be a built-in category icon (the
			// picker, ut-docs#1844, or the auto-import placeholder,
			// ut-docs#1189), which never lived under
			// /public/assets/items/<id>/thumb.png. thumbnails[it.ID] is ""
			// for an item with no thumbnail row at all — the grid template's
			// onerror already hides a tile whose ImageURL 404s/is empty.
			ImageURL: thumbnails[it.ID],
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
		// idleResetURL (ut-docs#815 review finding, BLOCKER 2): the idle
		// timer sends this page back to /self-order, which Reset()s the
		// basket -- and with it the table a QR session bound
		// (registerSelfOrder). On a shop's own kiosk terminal that is exactly
		// right ("start fresh for the next customer"), but on a GUEST'S OWN
		// PHONE the default 60s of not touching the screen (reading the menu,
		// talking to the table) silently unbound their table and dropped the
		// next checkout back onto the card/contactless path their phone
		// cannot use. Carrying ?table= through the bounce re-binds the same
		// table on the way back in, where it is re-validated (enabled, still
		// exists) exactly as on the first scan -- this never resurrects a
		// table /self-order would refuse today.
		idleResetURL := "/self-order"
		if d.KioskEngine != nil {
			if tableID := d.KioskEngine.TableID(); tableID != "" {
				idleResetURL += "?table=" + url.QueryEscape(tableID)
			}
		}
		httpx.RenderPartial("ui/pages/self_order_shop.html", map[string]any{
			"title":         "Order here",
			"Categories":    cats,
			"idleResetSecs": d.CurrentState().KioskIdleResetSeconds,
			"idleResetURL":  idleResetURL,
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
			// ut-docs#2227: same parent-price defect as the cashier's
			// /api/pos/scan, and reachable here even though the shipped
			// grid never triggers it (self_order_grid.html's own
			// HasVariants check keeps a real tap off this path) — this
			// endpoint is anonymous and auth-exempt, so a crafted POST with
			// a variant-bearing parent code still reaches it directly.
			// Unlike the cashier path there is no picker to redirect to
			// here (no UI ever reaches this branch to render one into), so
			// this refuses the add instead of prompting — deliberately
			// asymmetric with the cashier fix, per the design note.
			// review finding, BLOCKER 1 (same as the cashier path): a
			// weight/price-embedded scale label (QtyFromCode) must be left
			// alone here too — there is no variant question this endpoint
			// can correctly ask for one, refusing it would just as wrongly
			// block a legitimate scale-label add.
			// ut-docs#2244: skip the repeat ItemVariantsFor round trip once
			// this session already found itemID variant-free — see
			// pos.Service.HasNoSellableVariants' doc comment for why this
			// can't reintroduce #2227 review finding F1.
			if base, ok := d.KioskEngine.ResolveBase(code); ok && base.VariantID == "" && base.ItemID != "" && !base.QtyFromCode && !d.KioskEngine.HasNoSellableVariants(base.ItemID) {
				variants, err := data.NewCatalogRepo(d.Db).ItemVariantsFor(r.Context(), base.ItemID)
				if err != nil {
					// Deliberately NOT memoized — a transient error must stay
					// retryable, not get cached as a false "no variants".
					renderKioskCartWithMessage(w, r, d, httpx.T(httpx.ResolveLocale(w, r), "modifiers.variant_unavailable"))
					return
				}
				if len(sellableVariants(variants)) > 0 {
					// review finding, non-blocker 3: "variant_unavailable"
					// ("the options just changed") is misleading here —
					// nothing changed, the item has always needed a variant
					// choice this anonymous endpoint can't make. Reuse the
					// existing "variant_required" key instead (no new i18n
					// key, already present in every locale).
					renderKioskCartWithMessage(w, r, d, httpx.T(httpx.ResolveLocale(w, r), "modifiers.variant_required"))
					return
				}
				d.KioskEngine.MarkNoSellableVariants(base.ItemID)
			}
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
		groups, err := data.NewModifierRepo(d.Db).ResolveGroupsForItem(r.Context(), itemID)
		if err != nil {
			http.Error(w, "failed to load customization options", http.StatusInternalServerError)
			return
		}
		// ut-docs#2228: same fix as the cashier picker (pos_modifiers_api.go)
		// — the kiosk picker also DISPLAYS a price, so it must resolve
		// through price_history like the basket does, not show the
		// configured base price.
		variants, err := data.NewCatalogRepo(d.Db).ItemVariantsForSale(r.Context(), itemID)
		if err != nil {
			http.Error(w, "failed to load customization options", http.StatusInternalServerError)
			return
		}
		httpx.RenderPartial("ui/partials/self_order_modifier_picker.html", map[string]any{
			"ItemID":   itemID,
			"Code":     code,
			"ItemName": base.Name,
			"Groups":   groups,
			"Variants": sellableVariants(variants),
		})(w, r)
	})

	mux.HandleFunc("POST /api/self-order/scan-with-modifiers", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := strings.TrimSpace(r.Form.Get("code"))
		itemID := strings.TrimSpace(r.Form.Get("itemId"))

		base, selected, userMsg, err := resolveAndValidateModifiers(r.Context(), d, d.KioskEngine, httpx.ResolveLocale(w, r), code, itemID, r.Form)
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
		// ut-docs#815 (review finding, BLOCKER 1): a table-bound session
		// (/self-order?table=<id> on a guest's own phone) stays dine-in.
		// SetOrderType -> applyTablePolicyLocked clears the basket's table
		// the moment no dine-in line remains (ADR-0073 Decision 5,
		// ut-docs#1355), and selfOrderForcesCounterCheckout reads exactly
		// that table to decide the checkout path -- so one tap on the cart's
		// Takeaway button silently unbound the table AND dropped the guest
		// back onto the card/contactless payment picker, on a phone with no
		// card terminal attached (reproduced: it completed a real sale).
		// Clamped here rather than by re-applying the table after the switch:
		// re-applying would leave an all-takeaway basket holding a table,
		// exactly the state ADR-0073 D5 forbids. The cart hides the toggle
		// for a table-bound session too (self_order_cart.html), same
		// UI-soft-gate + server-enforcement pairing ut-docs#1355 established
		// for the cashier's own table picker -- this surface is anonymous and
		// auth-exempt, so the UI alone can never be the enforcement point.
		if orderType == pos.OrderTypeTakeaway && d.KioskEngine.TableID() != "" {
			orderType = ""
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
	//
	// ut-docs#582: in "counter" payment mode there is nothing to pick --
	// the kiosk never takes payment -- so this renders the plain "place
	// order" confirm screen (self_order_counter_confirm.html) instead of
	// loading/offering payment methods at all. Checked FIRST, before the
	// ListActiveNonCashPaymentMethods call below, so a counter-mode kiosk
	// never even queries payment methods it will never show.
	mux.HandleFunc("GET /api/self-order/checkout", func(w http.ResponseWriter, r *http.Request) {
		if len(d.KioskEngine.Lines()) == 0 {
			http.Error(w, "basket is empty", http.StatusBadRequest)
			return
		}
		if selfOrderForcesCounterCheckout(d) {
			renderKioskCounterConfirmPicker(w, r, d)
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
		// ut-docs#582/#815: counter-order checkout is a COMPLETELY separate
		// path -- checked before any method/ListActiveNonCashPaymentMethods
		// code runs -- because it creates no sale/payment at all. Kiosk
		// (default) mode below this branch is byte-identical to before
		// these cards for a session with no table bound.
		if selfOrderForcesCounterCheckout(d) {
			completeCounterOrderCheckout(w, r, d)
			return
		}
		// ut-docs#1795: same canonicalization as the cashier tender/refund
		// paths (pos_api.go, refund_page.go) -- this handler shares the
		// same completeTender -> blockingPaymentEventWithResponseAndID /
		// fiscal.MethodKeyOKC sink. Today the exact-match whitelist check
		// just below (`m.ID == method`) happens to fail closed on a
		// differently-cased method rather than routing it anywhere, but
		// this surface is anonymous/auth-exempt (ADR-0020) -- it shouldn't
		// rely on that as its only defense against the same case-mismatch
		// class this card fixes elsewhere.
		method := strings.ToLower(strings.TrimSpace(r.Form.Get("method")))

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

		// ut-docs#2067: a kiosk is its own register (Settings → Registers
		// can pin it to a stock location), so the sale draws from THAT
		// location, falling back to Main when none is assigned — same
		// resolution as the cashier tender path.
		registerID := tillRegisterIDBestEffort(r.Context(), d)
		locID, err := pos.ResolveStockLocationID(r.Context(), d.Db, registerID)
		if err != nil {
			http.Error(w, "failed to prepare sale", http.StatusInternalServerError)
			return
		}
		saleLines, total, taxBlocked := kioskSaleLinesAndTotal(d, locID)
		if taxBlocked {
			// ut-docs#368 — same fail-closed rule as the cashier tender
			// path: a basket line whose registered tax plugin is broken
			// must not be rung up at a silently-wrong base rate. The
			// anonymous customer can't repair anything, so the message
			// points them to the counter.
			w.WriteHeader(http.StatusConflict)
			renderKioskPaymentPicker(w, r, d, methods, "selforder.checkout.tax_unavailable")
			return
		}
		if !total.IsPositive() {
			w.WriteHeader(http.StatusBadRequest)
			renderKioskPaymentPicker(w, r, d, methods, "selforder.checkout.invalid_method")
			return
		}

		// The EnsureRegister self-heal only remains for the ambiguous case
		// (two-plus registers, nothing persisted) — today's behaviour there;
		// otherwise the sale row records the same register the stock
		// location above was resolved from.
		if registerID == "" {
			registerID, err = repo.EnsureRegister(r.Context())
			if err != nil {
				http.Error(w, "failed to prepare sale", http.StatusInternalServerError)
				return
			}
		}

		allowNegative := d.CurrentState().AllowNegativeInventory
		if d.SyncPrimaryURL(r.Context()) != "" {
			allowNegative = true // a replica never gates on stock it doesn't own (ut-docs#404, ADR-0036) — same bypass as the cashier tender path
		}

		saleInput := pos.SaleInput{
			SaleType:     "sale",
			Currency:     d.CurrentState().Currency,
			TaxInclusive: d.CurrentState().TaxInclusive,
			// Same declared-offline signal the cashier tender path threads
			// into SaleInput.Offline (review of ut-docs#675, B1): the kiosk
			// page's hidden #selforder-offline-flag input — driven by
			// navigator.onLine, hx-include'd into the checkout form — read
			// with the same formFlagTruthy convention /api/pos/tender uses.
			// Without it a genuinely-offline kiosk burned the full 3s
			// fiscal.sign.ask budget on EVERY sale (the known-offline
			// short-circuit never fired), and the sale row's own
			// offline/sync flags were wrong too. The kiosk surfaces no
			// manual offline_override toggle, so only the browser signal is
			// consulted here.
			Offline: formFlagTruthy(r.Form.Get("offline")),
			Lines:   saleLines,
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
			var noReceipt *fiscalDeviceNoReceiptError
			var deviceRequired *fiscalDeviceRequiredError
			var fiscalNC *fiscalNeverConfiguredError
			var fiscalTF *fiscalTSEFailingError
			status := http.StatusBadRequest
			msgKey := "selforder.checkout.failed"
			switch {
			case errors.As(err, &declined):
				status = http.StatusPaymentRequired
				msgKey = "selforder.checkout.declined"
			case errors.As(err, &noReceipt), errors.As(err, &deviceRequired):
				// ut-docs#1779/#1768: the device may already have taken the
				// customer's money before answering with no receipt (or, for
				// #1768, no leg ever used the device at all) — an anonymous
				// kiosk customer can't check the device themselves, so (like
				// the fiscal hard gate below) this points them to the
				// counter rather than inviting a self-service retry that
				// risks a double charge.
				status = http.StatusConflict
				msgKey = "selforder.checkout.fiscal_device_no_receipt"
			case errors.As(err, &fiscalNC), errors.As(err, &fiscalTF):
				// DE+TR fiscal-signing-device hard gate (ADR-0048, fiscal.RequiresHardGate) — same fail-closed rule
				// as the cashier tender path, same shape as the blocked-tax
				// refusal above: the anonymous customer can't repair
				// anything, so the message points them to the counter.
				status = http.StatusConflict
				msgKey = "selforder.checkout.fiscal_blocked"
			}
			w.WriteHeader(status)
			renderKioskPaymentPicker(w, r, d, methods, msgKey)
			return
		}

		// ut-docs#1817: GetSaleDetailByID (not the narrower SaleTotals) so
		// the confirmation screen gets DisplayNo already resolved to
		// ReceiptNo when the sale has none, same convention as every other
		// display_no reader in this codebase.
		detail, ok, _ := repo.GetSaleDetailByID(r.Context(), saleID)
		receiptNo, displayNo := detail.ReceiptNo, detail.DisplayNo
		if !ok || receiptNo == "" {
			receiptNo, displayNo = saleID, saleID
		}
		// Customer order tracking QR (ut-docs#527) — best-effort: the sale
		// is committed, so a missing QR (no LAN-dialable address, encode
		// error) renders a plain confirmation, never an error.
		trackingQR, trackingURL := orderTrackingQRView(r, repo, receiptNo, httpx.ResolveLocale(w, r))
		httpx.RenderPartial("ui/partials/self_order_confirmation.html", map[string]any{
			"ReceiptNo":   receiptNo,
			"DisplayNo":   displayNo,
			"TrackingQR":  trackingQR,
			"TrackingURL": trackingURL,
			"CounterMode": false,
		})(w, r)
	})
}

// selfOrderForcesCounterCheckout reports whether THIS basket must check out
// as a kiosk_counter_orders row (ut-docs#582's "pay at counter" path)
// rather than attempting a sale/card payment. True whenever the till's
// own kiosk.payment_mode is "counter" (unchanged #582 behaviour), OR
// whenever the current self-order session is bound to a physical table
// (ut-docs#815) -- d.KioskEngine.TableID() is only ever non-empty when
// this session started at /self-order?table=<validEnabledTableID>
// (registerSelfOrder). A guest's own phone has no card terminal attached
// to it regardless of what the till itself is configured for, so a table
// session ALWAYS forces the counter path -- a deliberate Architect
// decision (ut-docs#815 brief), not a bug: the till's kiosk.payment_mode
// setting only ever governs a checkout with no table bound.
func selfOrderForcesCounterCheckout(d *common.Deps) bool {
	return d.CurrentState().KioskPaymentMode == common.KioskPaymentModeCounter || d.KioskEngine.TableID() != ""
}

// renderKioskCounterConfirmPicker renders the "pay at counter" confirm
// screen (ut-docs#582) — the counter-mode twin of renderKioskPaymentPicker
// above: no payment methods to choose, just one big "place order" button.
func renderKioskCounterConfirmPicker(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	httpx.RenderPartial("ui/partials/self_order_counter_confirm.html", map[string]any{
		"Total": d.KioskEngine.Basket().Total,
	})(w, r)
}

// counterOrderModifierNames flattens a basket line's chosen modifiers to
// their option names, the same shape kitchenItemsFor (kitchen_print.go)
// reads off data.SaleDetailLine.Modifiers — this basket line hasn't been
// through a sale yet, so it flattens straight from pos.BasketLine's own
// []data.SelectedModifier instead.
func counterOrderModifierNames(mods []data.SelectedModifier) []string {
	if len(mods) == 0 {
		return nil
	}
	names := make([]string, 0, len(mods))
	for _, m := range mods {
		names = append(names, m.OptionName)
	}
	return names
}

// completeCounterOrderCheckout is the entire "pay at counter" checkout path
// (ut-docs#582): unlike the kiosk (card/contactless) path above, it never
// builds a pos.SaleInput and never calls completeTender — there is no
// payment to take, so there must be no sale/payment row either. It records
// a kiosk_counter_orders row instead (internal/data/
// kiosk_counter_orders_repo.go), fires a best-effort kitchen ticket, clears
// the kiosk basket the same way GET /self-order does on every fresh visit
// (d.KioskEngine.Reset()), and renders the SAME self_order_confirmation.html
// partial the kiosk path uses, with CounterMode=true selecting the
// "selforder.confirm.counter_hint" copy instead of the payment-flow hint.
func completeCounterOrderCheckout(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	lines := d.KioskEngine.Lines()
	if len(lines) == 0 {
		http.Error(w, "basket is empty", http.StatusBadRequest)
		return
	}
	orderLines := make([]data.KioskCounterOrderLine, 0, len(lines))
	for _, l := range lines {
		orderLines = append(orderLines, data.KioskCounterOrderLine{
			Name:      l.Name,
			Qty:       l.Qty,
			Modifiers: counterOrderModifierNames(l.Modifiers),
		})
	}

	repo := data.NewKioskCounterOrdersRepo(d.Db)
	order, err := repo.Create(r.Context(), data.KioskCounterOrder{
		OrderType: d.KioskEngine.OrderType(),
		// TableID/TableLabel (ut-docs#815): "" for a plain kiosk-till
		// counter order (#582, unaffected) -- only set when this checkout
		// is forced by a table-bound session (selfOrderForcesCounterCheckout).
		// Read straight off the engine rather than re-querying the tables
		// repo: SetTable (registerSelfOrder) already resolved and cached
		// both at session start, same as CustomerID/CustomerName elsewhere
		// on this same Service.
		TableID:    d.KioskEngine.TableID(),
		TableLabel: d.KioskEngine.TableLabel(),
		Lines:      orderLines,
	})
	if err != nil {
		http.Error(w, "failed to place order", http.StatusInternalServerError)
		return
	}

	// Same post-checkout reset the kiosk (card/contactless) path gets via
	// completeTender's own engine.Reset() call — a counter order is just as
	// "done" from the kiosk's point of view as a paid sale.
	d.KioskEngine.Reset()

	printCounterOrderTicketAsync(d, order)

	httpx.RenderPartial("ui/partials/self_order_confirmation.html", map[string]any{
		"ReceiptNo":   order.ID,
		"DisplayNo":   order.DisplayNo,
		"TrackingQR":  "",
		"TrackingURL": "",
		"CounterMode": true,
	})(w, r)
}

// printCounterOrderTicketAsync sends a kitchen ticket for a counter order
// without ever blocking checkout — mirrors printKitchenAsync's shape
// (kitchen_print.go: goroutine, d.AsyncWork tracked, its own timeout,
// best-effort, never fails or delays the order) but builds
// print.KitchenTicket/print.KitchenItem DIRECTLY from the counter order's
// own fields instead of going through buildKitchenTicket/buildKitchenTargets
// — both of those hard-require a data.SaleDetail via GetSaleDetail(receiptNo),
// which does not exist for a counter order (there is no sale row at all).
// v1 scope (explicit non-goal per the card): one ticket to the legacy
// printer.kitchen_addr only — no per-station routing.
func printCounterOrderTicketAsync(d *common.Deps, order data.KioskCounterOrder) {
	d.AsyncWork.Add(1)
	go func() {
		defer d.AsyncWork.Done()
		ctx, cancel := context.WithTimeout(context.Background(), printAsyncTimeout)
		defer cancel()
		cfg, cfgErr := printerConfigChecked(ctx, d)
		if cfgErr != nil || !cfg.KitchenEnabled() {
			// No legacy kitchen printer configured (or the settings read
			// itself failed) — best-effort, silently no-op, same as
			// printKitchenAsync's own "nothing to send" path. A counter
			// order carries no /orders warning flag to set (it isn't a
			// sale), so there is nothing further to record either way.
			return
		}
		// locale here is the SHOP's default, matching Station/OrderLabel/
		// OrderType below — ut-docs#2221 moved Qty's formatting from write
		// time (completeCounterOrderCheckout, keyed on the ordering
		// customer's own request locale) to here, so it now follows the
		// same locale as every other field on this ticket rather than
		// being the one odd one out keyed on whoever happened to place the
		// order. FormatQtyLatin (never digit-shaped) is still correct
		// regardless of locale — an ESC/POS printer can't render
		// Arabic-Indic glyphs — only the decimal/grouping convention for a
		// weighed line's fractional qty can change here.
		locale := httpx.DefaultLocale()
		ticket := kitchenTicketForCounterOrder(order, cfg, locale)
		tr, err := print.TransportForAddress(cfg.KitchenAddress)
		if err != nil || tr == nil {
			return
		}
		_ = tr.Print(ctx, print.RenderKitchenTicket(ticket))
	}()
}

// kitchenTicketForCounterOrder builds the print.KitchenTicket for a counter
// order — extracted out of printCounterOrderTicketAsync's own goroutine so
// it's unit-testable directly (byte-comparison tests still cover the
// goroutine end-to-end via printCounterOrderTicketAsync itself). Table
// (ut-docs#815) mirrors kitchenTicketFor's own detail.TableLabel handling
// (kitchen_print.go, ut-docs#820) exactly: the raw, already-resolved table
// label, "" for a plain kiosk-till counter order with no table --
// print.KitchenTicket already treats "" as "print nothing" on that line.
func kitchenTicketForCounterOrder(order data.KioskCounterOrder, cfg print.Config, locale string) print.KitchenTicket {
	items := make([]print.KitchenItem, 0, len(order.Lines))
	for _, l := range order.Lines {
		items = append(items, print.KitchenItem{
			Qty:       httpx.FormatQtyLatin(l.Qty, locale),
			Name:      l.Name,
			Modifiers: l.Modifiers,
		})
	}
	return print.KitchenTicket{
		Station:    kitchenTicketText(locale, cfg.Charset, "kitchen.ticket.station_default"),
		OrderNo:    order.DisplayNo,
		OrderLabel: kitchenTicketText(locale, cfg.Charset, "kitchen.ticket.order_label"),
		OrderType:  kitchenOrderTypeLabel(locale, cfg.Charset, order.OrderType),
		Table:      order.TableLabel,
		Timestamp:  order.CreatedAt,
		Charset:    cfg.Charset,
		Items:      items,
	}
}

// kioskSaleLinesAndTotal converts the current basket into SaleLineInput rows
// and computes the payable total, mirroring the cashier tender handler's
// subtotal/tax math (/api/pos/tender in pos_api.go) exactly — a kiosk
// checkout must land on the same total a cashier would for an identical
// basket. Kiosk sales never carry a sale-level discount (no UI surfaces one
// to an anonymous customer), so this is deliberately simpler than the
// cashier path, which also honors a client- or basket-supplied discount.
func kioskSaleLinesAndTotal(d *common.Deps, locID string) ([]pos.SaleLineInput, money.Money, bool) {
	var saleLines []pos.SaleLineInput
	subtotal, taxTotal := money.Zero, money.Zero
	for _, l := range d.KioskEngine.Lines() {
		// Same resolution as the cashier tender handler (pos_api.go) —
		// required by this function's own invariant above. taxBlocked is
		// the same ut-docs#368 fail-closed signal the cashier path honors:
		// a line whose registered tax plugin is broken must not be sold at
		// a silently-wrong base rate on this surface either.
		taxBP, taxBlocked := d.KioskEngine.EffectiveLineTaxRateBP(l)
		if taxBlocked {
			return nil, money.Zero, true
		}
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
			OrderType:          l.OrderType, // ADR-0073: kiosk lines inherit the kiosk's whole-order default
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
	return saleLines, total, false
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
		// CounterMode re-labels the cart's own call-to-action (review
		// finding, ut-docs#582): "selforder.checkout" reads as a neutral
		// "Checkout" in English, but its ar/fa/tr translations literally
		// mean "Pay" (الدفع / پرداخت / Ödeme). In counter mode the kiosk
		// never takes payment, so on those locales the button promised
		// something the next screen immediately contradicts. Reuses the
		// existing "selforder.counter.place_order" key rather than adding a
		// new one, so this costs no extra language-pack follow-up.
		//
		// ut-docs#815 (review finding): selfOrderForcesCounterCheckout, not
		// the till's payment mode alone — a table-bound session never takes
		// payment on the phone either, so its cart button must not promise
		// one (in ar/fa/tr "selforder.checkout" literally reads "Pay").
		"CounterMode": selfOrderForcesCounterCheckout(d),
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
