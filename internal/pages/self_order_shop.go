package pages

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
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
	ImageURL     string
}

// loadShopItems returns every active catalog item as a kiosk browse tile,
// optionally filtered to items whose name contains q (case-insensitive,
// in Go rather than SQL — single-shop catalogs are small enough that this
// is simpler and safer than a second hand-written search query to keep in
// sync with the admin catalog listing).
func loadShopItems(ctx context.Context, d *common.Deps, q string) ([]shopItem, error) {
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

	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]shopItem, 0, len(items))
	for _, it := range items {
		if q != "" && !strings.Contains(strings.ToLower(it.Name), q) {
			continue
		}
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
		out = append(out, shopItem{
			ItemID:       it.ID,
			Name:         it.Name,
			Description:  it.Description,
			CategoryID:   categoryID,
			Code:         code,
			PriceMinor:   it.BasePrice,
			HasModifiers: hasMods[it.ID],
			ImageURL:     "/public/assets/items/" + it.ID + "/thumb.png",
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
		httpx.RenderPartial("ui/pages/self_order_shop.html", map[string]any{
			"title":         "Order here",
			"Categories":    cats,
			"idleResetSecs": d.CurrentState().KioskIdleResetSeconds,
		})(w, r)
	})

	// Cart-only render, for the page's own hx-trigger="load" fragment —
	// same pattern index.html uses for /ui/basket.
	mux.HandleFunc("GET /api/self-order/cart", func(w http.ResponseWriter, r *http.Request) {
		renderKioskCart(w, r, d)
	})

	mux.HandleFunc("GET /api/self-order/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		items, err := loadShopItems(r.Context(), d, q)
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
			// Item resolution + add ONLY — no promo-code-via-code fallback,
			// no scan-to-refund, no customer-barcode lookup. Those are
			// cashier-facing behaviors on /api/pos/scan that must not be
			// reachable from this anonymous surface.
			d.Engine.ScanQtyWithResult(code, qty)
		}
		renderKioskCart(w, r, d)
	})

	mux.HandleFunc("GET /api/self-order/modifiers", func(w http.ResponseWriter, r *http.Request) {
		itemID := strings.TrimSpace(r.URL.Query().Get("item"))
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		base, ok := d.Engine.ResolveBase(code)
		if !ok || itemID == "" {
			http.Error(w, "item not found", http.StatusNotFound)
			return
		}
		groups, err := data.NewModifierRepo(d.Db).ListGroupsForItem(r.Context(), itemID)
		if err != nil {
			http.Error(w, "failed to load customization options", http.StatusInternalServerError)
			return
		}
		httpx.RenderPartial("ui/partials/self_order_modifier_picker.html", map[string]any{
			"ItemID":   itemID,
			"Code":     code,
			"ItemName": base.Name,
			"Groups":   groups,
		})(w, r)
	})

	mux.HandleFunc("POST /api/self-order/scan-with-modifiers", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := strings.TrimSpace(r.Form.Get("code"))
		itemID := strings.TrimSpace(r.Form.Get("itemId"))

		base, selected, userMsg, err := resolveAndValidateModifiers(r.Context(), d, code, itemID, r.Form)
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
		d.Engine.AddLineWithModifiers(base, qty, selected)
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
			for _, l := range d.Engine.Basket().Lines {
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
		d.Engine.UpdateLineByKey(key, qty, 0)
		renderKioskCart(w, r, d)
	})

	mux.HandleFunc("POST /api/self-order/remove", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		key := strings.TrimSpace(r.Form.Get("key"))
		if key == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		d.Engine.RemoveLine(key)
		renderKioskCart(w, r, d)
	})
}

func renderKioskCart(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	renderKioskCartWithMessage(w, r, d, "")
}

func renderKioskCartWithMessage(w http.ResponseWriter, r *http.Request, d *common.Deps, message string) {
	b := d.Engine.Basket()
	if message != "" {
		b.ToastMessage = message
	}
	httpx.RenderPartial("ui/partials/self_order_cart.html", map[string]any{
		"Basket": b,
	})(w, r)
}
