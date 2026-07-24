package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

// resolveAndValidateModifiers resolves code/itemId to a base line and
// validates+prices a modifier submission entirely from server-loaded
// catalog data — never a client-submitted name or price, only an option
// ID. Shared by the cashier (auth-required) and kiosk (auth-exempt)
// customization flows so both get identical security guarantees from one
// code path, rather than two independently-maintained copies that could
// drift. userMsg is set (and err non-nil) for a validation failure the
// caller should show the requester (400); userMsg is empty with err
// non-nil for an unexpected/server error (500).
func resolveAndValidateModifiers(ctx context.Context, d *common.Deps, code, itemID string, form url.Values) (pos.BasketLine, []data.SelectedModifier, string, error) {
	base, ok := d.Engine.ResolveBase(code)
	if !ok {
		return pos.BasketLine{}, nil, "Item not found", errors.New("resolve base: not found")
	}
	if itemID == "" || base.ItemID != itemID {
		// code and itemId are two independent caller-supplied values (the
		// picker's hidden inputs); a manipulated or stale submission could
		// send a mismatched pair. See docs/code-reviews/2026-07-24-item-modifiers-cashier-ui.md.
		return pos.BasketLine{}, nil, "Item not found", errors.New("code/itemId mismatch")
	}

	groups, err := data.NewModifierRepo(d.Db).ListGroupsForItem(ctx, itemID)
	if err != nil {
		return pos.BasketLine{}, nil, "", fmt.Errorf("load customization options: %w", err)
	}

	var selected []data.SelectedModifier
	for _, g := range groups {
		chosen := form["mod_"+g.ID]
		if len(chosen) < g.MinSelect || len(chosen) > g.MaxSelect {
			return pos.BasketLine{}, nil, fmt.Sprintf("%s: choose between %d and %d", g.Name, g.MinSelect, g.MaxSelect), errors.New("selection count out of bounds")
		}
		for _, optID := range chosen {
			var match *data.ModifierOption
			for i := range g.Options {
				if g.Options[i].ID == optID {
					match = &g.Options[i]
					break
				}
			}
			if match == nil {
				return pos.BasketLine{}, nil, "Invalid customization selected", errors.New("unknown option id")
			}
			selected = append(selected, data.SelectedModifier{
				GroupID:         g.ID,
				OptionID:        match.ID,
				GroupName:       g.Name,
				OptionName:      match.Name,
				PriceDeltaMinor: match.PriceDeltaMinor,
			})
		}
	}
	return base, selected, "", nil
}

// registerPOSModifiersAPI wires the cashier's item-customization step
// (ADR-0020): tapping a button whose item has modifier groups (extra
// shot, bread choice, ...) opens this picker instead of adding straight
// to the basket; submitting it adds the line with the chosen
// customizations applied. The kiosk equivalent (self_order_shop.go) reuses
// resolveAndValidateModifiers above but renders its own locked-down cart
// view, not this package's cashier ui.BasketView.
func registerPOSModifiersAPI(mux *http.ServeMux, d *common.Deps) {
	modRepo := data.NewModifierRepo(d.Db)

	mux.HandleFunc("GET /ui/pos/modifiers", func(w http.ResponseWriter, r *http.Request) {
		itemID := strings.TrimSpace(r.URL.Query().Get("item"))
		code := strings.TrimSpace(r.URL.Query().Get("code"))

		base, ok := d.Engine.ResolveBase(code)
		if !ok || itemID == "" {
			http.Error(w, "item not found", http.StatusNotFound)
			return
		}
		groups, err := modRepo.ListGroupsForItem(r.Context(), itemID)
		if err != nil {
			http.Error(w, "failed to load customization options", http.StatusInternalServerError)
			return
		}

		httpx.RenderPartial("ui/partials/modifier_picker.html", map[string]any{
			"ItemID":   itemID,
			"Code":     code,
			"ItemName": base.Name,
			"Groups":   groups,
		})(w, r)
	})

	mux.HandleFunc("POST /api/pos/scan-with-modifiers", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := strings.TrimSpace(r.Form.Get("code"))
		itemID := strings.TrimSpace(r.Form.Get("itemId"))

		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, _ := ui.NewBasketView(funcs)
		toast := func(msg string) {
			w.WriteHeader(http.StatusBadRequest)
			b := d.Engine.Basket()
			b.ToastMessage = msg
			_ = basketView.Render(w, &b)
		}

		base, selected, userMsg, err := resolveAndValidateModifiers(r.Context(), d, code, itemID, r.Form)
		if err != nil {
			if userMsg == "" {
				http.Error(w, "failed to load customization options", http.StatusInternalServerError)
				return
			}
			toast(userMsg)
			return
		}

		qty := 1.0
		if v := r.Form.Get("qty"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				qty = f
			}
		}

		d.Engine.AddLineWithModifiers(base, qty, selected)
		b := d.Engine.Basket()
		_ = basketView.Render(w, &b)
	})
}
