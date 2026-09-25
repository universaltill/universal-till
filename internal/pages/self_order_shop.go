package pages

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/barcode"
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

	// The next three lookups are chunked (ut-docs#2451, mirroring
	// ui.ButtonStore.LoadAllActive's ut-docs#2318 fix): SQLite's
	// bind-variable ceiling (32766) is well within plausible active-catalog
	// sizes when called unchunked with the WHOLE id set — ItemIDsWithModifiers
	// alone binds 2 args per id, so it starts failing past ~16,383 active
	// items. Chunking keeps every call comfortably under the ceiling
	// regardless of catalog size, and merging per-chunk results means a
	// failure now degrades only the chunk that failed, not the whole kiosk
	// grid.
	idChunks := data.ChunkStrings(ids, data.IDChunkSize)
	modifierRepo := data.NewModifierRepo(d.Db)
	// Unsized, like LoadAllActive's own hasMods/hasVariants (buttons.go):
	// on a typical catalog only a handful of items actually have
	// modifiers/variants, so pre-sizing to len(ids) would over-allocate on
	// every kiosk grid load, including on the Pi-class hardware this page
	// targets.
	hasMods := map[string]bool{}
	for _, chunk := range idChunks {
		m, err := modifierRepo.ItemIDsWithModifiers(ctx, chunk)
		if err != nil {
			// Previously silently discarded (`_`) — now logged like the
			// other two lookups below, since silence is exactly the failure
			// mode ut-docs#2451 exists to remove: without it, a tile that
			// needs a modifier prompt would add straight to the cart with
			// no prompt at all, and no error anywhere would say why.
			logging.L().Warnf("kiosk: load items-with-modifiers failed for a batch of %d item(s), those tiles fall back to plain add-to-basket: %v", len(chunk), err)
			continue
		}
		data.MergeMapInto(hasMods, m)
	}
	hasVariants := map[string]bool{}
	for _, chunk := range idChunks {
		m, err := repo.ItemIDsWithVariants(ctx, chunk)
		if err != nil {
			// Same reasoning as ButtonStore.Load's own guard (ut-docs#2209
			// review, finding 5): on this error every kiosk tile in the
			// failed batch reverts to adding the PARENT base price, so it
			// must never fail silently.
			logging.L().Warnf("kiosk: load items-with-variants failed for a batch of %d item(s), those tiles fall back to parent-price add (ut-docs#2209): %v", len(chunk), err)
			continue
		}
		data.MergeMapInto(hasVariants, m)
	}
	currentPrices := make(map[string]int64, len(ids))
	for _, chunk := range idChunks {
		p, err := repo.ItemCurrentPrices(ctx, chunk)
		if err != nil {
			// Same non-fatal-but-loud treatment as hasVariants above
			// (ut-docs#2258): on this error every kiosk tile in the failed
			// batch falls back to the STALE configured base_price
			// it.BasePrice already carries below.
			logging.L().Warnf("kiosk: load item current prices failed for a batch of %d item(s), those tiles fall back to raw base_price (ut-docs#2258): %v", len(chunk), err)
			continue
		}
		data.MergeMapInto(currentPrices, p)
	}
	thumbnails, _ := repo.ItemThumbnails(ctx) // best-effort: a read error just means every tile falls back to no-image, same as a missing row

	// ut-docs#2497: same round-trip-with-the-scan-resolver requirement as
	// ui.ButtonStore.LoadAllActive/SearchSellable (see their comment for
	// the full rationale) — /api/self-order/scan resolves through the same
	// POSRepo.ResolveShortcutLineDecoded, whose raw-barcode tier only
	// matches a code that decodes under the shop's CURRENTLY ENABLED
	// symbologies. Fetched once, shop-wide, above the loop.
	enabledIDs, _ := data.NewSettingsRepo(d.Db).EnabledBarcodeSymbologies(ctx)

	out := make([]shopItem, 0, len(items))
	for _, it := range items {
		code := it.SKU
		if bcs := barcodes[it.ID]; len(bcs) > 0 {
			if _, ok := barcode.Default().Match(enabledIDs, bcs[0]); ok {
				code = bcs[0] // primary first, per CatalogRepo.ItemBarcodes ordering
			}
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
		// walk-up basket -- and used to drop the table a QR session bound
		// (registerSelfOrder). On a shop's own kiosk terminal that is exactly
		// right ("start fresh for the next customer"), but on a GUEST'S OWN
		// PHONE the default 60s of not touching the screen (reading the menu,
		// talking to the table) silently unbound their table and dropped the
		// next checkout back onto the card/contactless path their phone
		// cannot use. Carrying ?table= through the bounce lands the guest
		// back on their own table -- since ADR-0103 (ut-docs#2261) that
		// RESUMES their per-session basket via the session cookie rather
		// than re-binding a wiped one, and the table is re-validated
		// (enabled, still exists) exactly as on the first scan -- this never
		// resurrects a table /self-order would refuse today.
		idleResetURL := "/self-order"
		if eng := selfOrderEngine(d, r); eng != nil {
			if tableID := eng.TableID(); tableID != "" {
				idleResetURL += "?table=" + url.QueryEscape(tableID)
			}
		}
		httpx.RenderPartial("ui/pages/self_order_shop.html", map[string]any{
			"title":         httpx.T(httpx.RequestLocale(r), "page.title.order_here"),
			"Categories":    cats,
			"idleResetSecs": d.CurrentState().KioskIdleResetSeconds,
			"idleResetURL":  idleResetURL,
		})(w, r)
	})

	// Cart-only render, for the page's own hx-trigger="load" fragment —
	// same pattern index.html uses for /ui/basket.
	mux.HandleFunc("GET /api/self-order/cart", func(w http.ResponseWriter, r *http.Request) {
		renderKioskCart(w, r, d, selfOrderEngine(d, r))
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
		eng := selfOrderEngine(d, r)
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
			if base, ok := eng.ResolveBase(code); ok && base.VariantID == "" && base.ItemID != "" && !base.QtyFromCode && !eng.HasNoSellableVariants(base.ItemID) {
				variants, err := data.NewCatalogRepo(d.Db).ItemVariantsFor(r.Context(), base.ItemID)
				if err != nil {
					// Deliberately NOT memoized — a transient error must stay
					// retryable, not get cached as a false "no variants".
					renderKioskCartWithMessage(w, r, d, eng, httpx.T(httpx.ResolveLocale(w, r), "modifiers.variant_unavailable"))
					return
				}
				if len(sellableVariants(variants)) > 0 {
					// review finding, non-blocker 3: "variant_unavailable"
					// ("the options just changed") is misleading here —
					// nothing changed, the item has always needed a variant
					// choice this anonymous endpoint can't make. Reuse the
					// existing "variant_required" key instead (no new i18n
					// key, already present in every locale).
					renderKioskCartWithMessage(w, r, d, eng, httpx.T(httpx.ResolveLocale(w, r), "modifiers.variant_required"))
					return
				}
				eng.MarkNoSellableVariants(base.ItemID)
			}
			// Item resolution + add ONLY — no promo-code-via-code fallback,
			// no scan-to-refund, no customer-barcode lookup. Those are
			// cashier-facing behaviors on /api/pos/scan that must not be
			// reachable from this anonymous surface.
			eng.ScanQtyWithResult(code, qty)
		}
		renderKioskCart(w, r, d, eng)
	})

	mux.HandleFunc("GET /api/self-order/modifiers", func(w http.ResponseWriter, r *http.Request) {
		itemID := strings.TrimSpace(r.URL.Query().Get("item"))
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		base, ok := selfOrderEngine(d, r).ResolveBase(code)
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
		eng := selfOrderEngine(d, r)

		base, selected, userMsg, err := resolveAndValidateModifiers(r.Context(), d, eng, httpx.ResolveLocale(w, r), code, itemID, r.Form)
		if err != nil {
			if userMsg == "" {
				http.Error(w, "failed to load customization options", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			renderKioskCartWithMessage(w, r, d, eng, userMsg)
			return
		}
		qty := 1.0
		if v := r.Form.Get("qty"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				qty = f
			}
		}
		eng.AddLineWithModifiers(base, qty, selected)
		renderKioskCart(w, r, d, eng)
	})

	// Qty-only line edit — deliberately no discount field at all (unlike
	// /api/pos/line's cashier-facing free-text discount, which would let
	// an anonymous customer manually cut their own bill). The +/- stepper
	// sends a relative delta (avoids needing template-side arithmetic to
	// compute an absolute value); an absolute qty is still accepted for
	// any future direct-entry UI.
	mux.HandleFunc("POST /api/self-order/line", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		eng := selfOrderEngine(d, r)
		key := strings.TrimSpace(r.Form.Get("key"))
		if key == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		qty := 0.0
		if v := strings.TrimSpace(r.Form.Get("delta")); v != "" {
			delta, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(delta) || math.IsInf(delta, 0) {
				http.Error(w, "invalid delta", http.StatusBadRequest)
				return
			}
			for _, l := range eng.Basket().Lines {
				if l.LineKey == key {
					qty = l.Qty + delta
					break
				}
			}
			if qty < 0 {
				qty = 0
			}
		} else if v := r.Form.Get("qty"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && !math.IsInf(f, 0) {
				qty = f
			}
		}
		eng.UpdateLineByKey(key, qty, 0)
		renderKioskCart(w, r, d, eng)
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
		eng := selfOrderEngine(d, r)
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
		if orderType == pos.OrderTypeTakeaway && eng.TableID() != "" {
			orderType = ""
		}
		eng.SetOrderType(orderType)
		renderKioskCart(w, r, d, eng)
	})

	mux.HandleFunc("POST /api/self-order/remove", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		eng := selfOrderEngine(d, r)
		key := strings.TrimSpace(r.Form.Get("key"))
		if key == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		eng.RemoveLine(key)
		renderKioskCart(w, r, d, eng)
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
		eng := selfOrderEngine(d, r)
		if len(eng.Lines()) == 0 {
			http.Error(w, "basket is empty", http.StatusBadRequest)
			return
		}
		if selfOrderForcesCounterCheckout(d, eng) {
			renderKioskCounterConfirmPicker(w, r, eng)
			return
		}
		methods, err := repo.ListActiveNonCashPaymentMethods(r.Context())
		if err != nil {
			http.Error(w, "failed to load payment methods", http.StatusInternalServerError)
			return
		}
		renderKioskPaymentPicker(w, r, eng, methods, "")
	})

	mux.HandleFunc("POST /api/self-order/checkout", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		// eng is this request's basket; token is non-empty only when it is
		// a table-QR guest session (ADR-0103), which a completed checkout
		// must remove rather than reset (releaseSelfOrderSession).
		eng, token := selfOrderSession(d, r)
		// ut-docs#582/#815: counter-order checkout is a COMPLETELY separate
		// path -- checked before any method/ListActiveNonCashPaymentMethods
		// code runs -- because it creates no sale/payment at all. Kiosk
		// (default) mode below this branch is byte-identical to before
		// these cards for a session with no table bound.
		if selfOrderForcesCounterCheckout(d, eng) {
			completeCounterOrderCheckout(w, r, d, eng, token)
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

		lines := eng.Lines()
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
			renderKioskPaymentPicker(w, r, eng, methods, "selforder.checkout.invalid_method")
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
		saleLines, total, taxBlocked := kioskSaleLinesAndTotal(d, eng, lines, locID)
		if taxBlocked {
			// ut-docs#368 — same fail-closed rule as the cashier tender
			// path: a basket line whose registered tax plugin is broken
			// must not be rung up at a silently-wrong base rate. The
			// anonymous customer can't repair anything, so the message
			// points them to the counter.
			w.WriteHeader(http.StatusConflict)
			renderKioskPaymentPicker(w, r, eng, methods, "selforder.checkout.tax_unavailable")
			return
		}
		if !total.IsPositive() {
			w.WriteHeader(http.StatusBadRequest)
			renderKioskPaymentPicker(w, r, eng, methods, "selforder.checkout.invalid_method")
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
			CustomerID:             eng.CustomerID(),
			OrderType:              eng.OrderType(),
			AllowNegativeInventory: allowNegative,
			ActorID:                "kiosk",
		}
		saleID, err := completeTender(r.Context(), d, eng, repo, saleInput, saleInput.Payments, "kiosk", kitchenDeltaFilter(lines))
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
			renderKioskPaymentPicker(w, r, eng, methods, msgKey)
			return
		}

		// The sale is committed and completeTender has Reset() the engine;
		// a table-QR session (token != "") is additionally removed from the
		// manager and its cookie cleared (ADR-0103 D5) so the table is free
		// at once — a no-op for the walk-up KioskEngine path. In practice a
		// table-bound session always takes the counter branch above
		// (selfOrderForcesCounterCheckout), so this is the defensive half.
		releaseSelfOrderSession(w, d, token)

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
// (ut-docs#815) -- eng.TableID() is only ever non-empty when this request's
// basket (selfOrderEngine: the guest's own per-table session since
// ADR-0103) started at /self-order?table=<validEnabledTableID>
// (registerSelfOrder). A guest's own phone has no card terminal attached
// to it regardless of what the till itself is configured for, so a table
// session ALWAYS forces the counter path -- a deliberate Architect
// decision (ut-docs#815 brief), not a bug: the till's kiosk.payment_mode
// setting only ever governs a checkout with no table bound.
func selfOrderForcesCounterCheckout(d *common.Deps, eng *pos.Service) bool {
	return d.CurrentState().KioskPaymentMode == common.KioskPaymentModeCounter || eng.TableID() != ""
}

// renderKioskCounterConfirmPicker renders the "pay at counter" confirm
// screen (ut-docs#582) — the counter-mode twin of renderKioskPaymentPicker
// above: no payment methods to choose, just one big "place order" button.
func renderKioskCounterConfirmPicker(w http.ResponseWriter, r *http.Request, eng *pos.Service) {
	httpx.RenderPartial("ui/partials/self_order_counter_confirm.html", map[string]any{
		"Total": eng.Basket().Total,
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

// counterOrderTicketFromSnapshot (ut-docs#2703) builds the kiosk order's
// lines -- what the kitchen ticket prints -- 1:1 and in order from
// snap.Lines, and returns markSent, which records every one of those same
// lines as fully sent (SnapshotLine.KitchenSentQty = Qty). Ticket and marks
// share one source by construction, so a line can be marked sent only if
// the ticket showed it.
func counterOrderTicketFromSnapshot(snap *pos.BasketSnapshot) ([]data.KioskCounterOrderLine, func()) {
	lines := make([]data.KioskCounterOrderLine, 0, len(snap.Lines))
	for _, l := range snap.Lines {
		lines = append(lines, data.KioskCounterOrderLine{
			Name:      l.Name,
			Qty:       l.Qty,
			Modifiers: counterOrderModifierNames(l.Modifiers),
		})
	}
	n := len(lines)
	return lines, func() {
		for i := 0; i < n && i < len(snap.Lines); i++ {
			snap.Lines[i].KitchenSentQty = snap.Lines[i].Qty
		}
	}
}

// completeCounterOrderCheckout is the entire "pay at counter" checkout path
// (ut-docs#582, reshaped by ut-docs#2703): unlike the kiosk
// (card/contactless) path above it takes no payment, so it creates no sale.
//
// ut-docs#2703 (product owner, 2026-09-25): "when the customer still didn't
// pay for the order and waits for pay at the counter, it should be exactly
// the same as a hold order, not a placed one." So the kiosk basket is
// parked as a HELD SALE -- the same held_sales row, payload
// (pos.BasketSnapshot) and write-through (heldSaleWriteThrough, ADR-0093)
// the till's own Hold uses. It then shows in Open orders, the parked-orders
// popup and the On hold strip, the cashier resumes it with the existing
// resume path, and payment goes through the normal tender -- a real sale,
// fiscally signed, in day-close. Before this, "Mark collected" closed the
// order with no money taken at all.
//
// A kiosk_counter_orders row is still written (status "held"):
// the "C-" order number sequence lives there, and it records what was
// ordered. It is not the payable object and never shows on the legacy
// staff board.
//
// The snapshot carries the order number (BasketSnapshot.DisplayNo) so the
// paid sale's display_no, receipt and kitchen ticket show the number the
// customer holds. Kitchen ticket timing: a walk-up pay-at-counter order
// prints at payment, like any till sale (completeTender); a table-QR order
// (ut-docs#815) prints now -- dine-in guests eat before they pay. The
// checkout print is SYNCHRONOUS (printCounterOrderTicket, short timeout) so
// its outcome is known before the order is parked: only when it actually
// reached the printer is every line marked sent (SnapshotLine.
// KitchenSentQty), and the tender path then prints only what the cashier
// adds later. A printer that is down, or no legacy kitchen printer at all,
// leaves the lines unsent, so the whole order prints at payment instead of
// being lost -- and the guest is told it is made after payment.
//
// The basket is then cleared -- the walk-up KioskEngine by Reset(); a
// table-QR guest session (token != "", ADR-0103 D5) removed from the
// manager with its cookie cleared -- and the customer sees the SAME
// self_order_confirmation.html partial with their C-number.
func completeCounterOrderCheckout(w http.ResponseWriter, r *http.Request, d *common.Deps, eng *pos.Service, token string) {
	// ONE read of the basket (round-2 review of ut-docs#2703): the order
	// row, the kitchen ticket, the "already sent" marks and the parked
	// payload all come from this snapshot. Reading the engine twice let an
	// add landing in between be parked as sent although no ticket showed it.
	snap := eng.Snapshot()
	if len(snap.Lines) == 0 {
		http.Error(w, "basket is empty", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	tableBound := snap.TableID != ""
	orderLines, markSentToKitchen := counterOrderTicketFromSnapshot(&snap)

	// One id for both rows, so a held sale can always be traced back to
	// the kiosk order it came from. "hold-" like every other held sale id;
	// a uuid rather than hold-<nanos> because several table-QR sessions
	// can check out at the same instant.
	heldID := "hold-" + uuid.NewString()
	order, err := data.NewKioskCounterOrdersRepo(d.Db).Create(ctx, data.KioskCounterOrder{
		ID:        heldID,
		Status:    data.KioskCounterOrderStatusHeld,
		OrderType: eng.OrderType(),
		// TableID/TableLabel (ut-docs#815): "" for a walk-up kiosk order;
		// set when a table-bound session forced this path. From the
		// snapshot; SetTable already resolved them at session start.
		TableID:    snap.TableID,
		TableLabel: snap.TableLabel,
		Lines:      orderLines,
	})
	if err != nil {
		logging.L().Errorf("self-order counter checkout: record order: %v", err)
		http.Error(w, "failed to place order", http.StatusInternalServerError)
		return
	}

	snap.DisplayNo = order.DisplayNo
	// Printed before parking so the held payload records the real outcome.
	// Chosen over an async print + success callback: the callback would
	// have to rewrite a held row that may already have been pushed to the
	// main till (heldSaleWriteThrough), a second networked write for one
	// flag. The cost is that the guest waits up to
	// counterOrderTicketTimeout on a dead printer. Known edge: if parking
	// then fails, the kitchen has a ticket for an order the guest is told
	// failed -- a local DB write failure, and a duplicate/stray ticket is
	// recoverable where a lost one is not.
	sentToKitchen := false
	if tableBound {
		sent, perr := printCounterOrderTicket(ctx, d, order)
		if perr != nil {
			logging.L().Warnf("self-order counter checkout: kitchen ticket for %s not printed, it will print at payment: %v", order.DisplayNo, perr)
		}
		sentToKitchen = sent
	}
	if sentToKitchen {
		markSentToKitchen()
	}
	payload, err := json.Marshal(snap)
	if err != nil {
		http.Error(w, "failed to place order", http.StatusInternalServerError)
		return
	}
	held := data.HeldSale{
		ID:         heldID,
		Label:      counterOrderHeldLabel(order.DisplayNo, snap.OrderType),
		TotalMinor: snap.Total.Minor(),
		LineCount:  len(snap.Lines),
		Payload:    string(payload),
		TableID:    snap.TableID,
	}
	// Offline-first: on a replica this pushes the order to the main till
	// (so it is listed and payable there, like any parked order); any
	// failure reaching it keeps the row local -- the checkout never waits
	// on the network beyond the proxy's own short budget.
	if _, err := heldSaleWriteThrough(ctx, d, data.NewHeldSalesRepo(d.Db), held); err != nil {
		// The kiosk_counter_orders row above stays behind in "held" status
		// with no held sale: invisible everywhere, it only burns one C-
		// number. The customer is told the order failed, so nothing is
		// promised that the till cannot find.
		logging.L().Errorf("self-order counter checkout: park order %s as held sale: %v", order.DisplayNo, err)
		http.Error(w, "failed to place order", http.StatusInternalServerError)
		return
	}

	// A table-QR session is removed outright (ADR-0103 D5; header write,
	// so it runs before RenderPartial below); the walk-up kiosk basket is
	// reset, same as completeTender does after a paid kiosk sale.
	if token != "" {
		releaseSelfOrderSession(w, d, token)
	} else {
		eng.Reset()
	}

	httpx.RenderPartial("ui/partials/self_order_confirmation.html", map[string]any{
		"ReceiptNo":   order.ID,
		"DisplayNo":   order.DisplayNo,
		"TrackingQR":  "",
		"TrackingURL": "",
		"CounterMode": true,
		"PayFirst":    !sentToKitchen,
	})(w, r)
}

// counterOrderHeldLabel names a parked pay-at-counter order the way the
// cashier will look for it on Open orders: the customer's order number
// plus the order type ("C-12 · Takeaway"). The table is not repeated here
// -- held_sales.table_id carries it and every list shows it in its own
// column/chip. Rendered in the SHOP's default locale, not the kiosk
// customer's: the label is stored text read by staff, same choice as the
// kitchen ticket (printCounterOrderTicket).
func counterOrderHeldLabel(displayNo, orderType string) string {
	locale := httpx.DefaultLocale()
	key := "selforder.order_type.dine_in"
	switch orderType {
	case pos.OrderTypeTakeaway:
		key = "basket.order_type.takeaway"
	case pos.OrderTypeMixed:
		key = "basket.order_type.mixed"
	}
	return displayNo + " · " + httpx.T(locale, key)
}

// counterOrderTicketTimeout bounds the synchronous checkout print of a
// table-QR order's kitchen ticket (ut-docs#2703): the guest is waiting on
// the confirmation screen, and a dead printer only means the ticket prints
// at payment instead.
var counterOrderTicketTimeout = 5 * time.Second

// printCounterOrderTicket sends a table-QR counter order's kitchen ticket
// and reports whether it actually reached the printer (ut-docs#2703: the
// caller marks the order's lines as sent only on true, so a failed print is
// retried at payment instead of lost). (false, nil) means there was nothing
// to print to -- no legacy kitchen printer configured -- and the tender
// path's routed print covers it. It builds print.KitchenTicket DIRECTLY
// from the counter order's own fields instead of going through
// buildKitchenTicket/buildKitchenTargets, which hard-require a sale
// (there is none until payment). v1 scope (explicit non-goal per the
// card): one ticket to the legacy printer.kitchen_addr only -- no
// per-station routing. Runs on a context detached from the request so a
// guest closing the page mid-print cannot cut a ticket in half.
func printCounterOrderTicket(ctx context.Context, d *common.Deps, order data.KioskCounterOrder) (bool, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), counterOrderTicketTimeout)
	defer cancel()
	cfg, err := printerConfigChecked(ctx, d)
	if err != nil {
		return false, err
	}
	if !cfg.KitchenEnabled() {
		return false, nil
	}
	// locale is the SHOP's default (ut-docs#2221), matching every other
	// field on the ticket -- see kitchenTicketForCounterOrder.
	locale := httpx.DefaultLocale()
	ticket := kitchenTicketForCounterOrder(order, cfg, locale)
	tr, err := print.TransportForAddress(cfg.KitchenAddress)
	if err != nil {
		return false, err
	}
	if tr == nil {
		return false, nil
	}
	if err := tr.Print(ctx, print.RenderKitchenTicket(ticket)); err != nil {
		return false, err
	}
	return true, nil
}

// kitchenTicketForCounterOrder builds the print.KitchenTicket for a counter
// order — extracted out of printCounterOrderTicket so
// it's unit-testable directly (byte-comparison tests still cover the
// print end-to-end via printCounterOrderTicket itself). Table
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

// kioskSaleLinesAndTotal converts lines -- the basket as the checkout
// handler read it, once (ut-docs#2703: the kitchen filter handed to
// completeTender is built from the same slice) -- into SaleLineInput rows
// and computes the payable total, mirroring the cashier tender handler's
// subtotal/tax math (/api/pos/tender in pos_api.go) exactly — a kiosk
// checkout must land on the same total a cashier would for an identical
// basket. Kiosk sales never carry a sale-level discount (no UI surfaces one
// to an anonymous customer), so this is deliberately simpler than the
// cashier path, which also honors a client- or basket-supplied discount.
func kioskSaleLinesAndTotal(d *common.Deps, eng *pos.Service, lines []pos.BasketLine, locID string) ([]pos.SaleLineInput, money.Money, bool) {
	var saleLines []pos.SaleLineInput
	subtotal, taxTotal := money.Zero, money.Zero
	for _, l := range lines {
		// Same resolution as the cashier tender handler (pos_api.go) —
		// required by this function's own invariant above. taxBlocked is
		// the same ut-docs#368 fail-closed signal the cashier path honors:
		// a line whose registered tax plugin is broken must not be sold at
		// a silently-wrong base rate on this surface either.
		taxBP, taxBlocked := eng.EffectiveLineTaxRateBP(l)
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

// renderKioskCart renders this request's basket (eng: selfOrderEngine's
// answer — the guest's own table session, or the walk-up KioskEngine).
func renderKioskCart(w http.ResponseWriter, r *http.Request, d *common.Deps, eng *pos.Service) {
	renderKioskCartWithMessage(w, r, d, eng, "")
}

func renderKioskCartWithMessage(w http.ResponseWriter, r *http.Request, d *common.Deps, eng *pos.Service, message string) {
	b := eng.Basket()
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
		"CounterMode": selfOrderForcesCounterCheckout(d, eng),
	})(w, r)
}

// renderKioskPaymentPicker renders the payment-method modal. errKey, if set,
// is an i18n key (not raw text — this is a public, anonymous-facing surface)
// shown as an inline error above the method list.
func renderKioskPaymentPicker(w http.ResponseWriter, r *http.Request, eng *pos.Service, methods []data.PaymentMethod, errKey string) {
	httpx.RenderPartial("ui/partials/self_order_payment_picker.html", map[string]any{
		"Methods": methods,
		"Total":   eng.Basket().Total,
		"ErrKey":  errKey,
	})(w, r)
}
