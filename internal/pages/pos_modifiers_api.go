package pages

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

// registerPOSModifiersAPI wires the item-customization step (ADR-0020):
// tapping a button whose item has modifier groups (extra shot, bread
// choice, ...) opens this picker instead of adding straight to the basket;
// submitting it adds the line with the chosen customizations applied.
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

		base, ok := d.Engine.ResolveBase(code)
		if !ok {
			toast("Item not found")
			return
		}
		if itemID == "" {
			toast("Item not found")
			return
		}

		// Server-authoritative validation and pricing — never trust a
		// client-submitted option name or price delta, only the id. A
		// manipulated form (e.g. adding an option id from a different
		// item, or resubmitting after the catalog changed) must be
		// rejected, not silently priced from whatever the client sent.
		groups, err := data.NewModifierRepo(d.Db).ListGroupsForItem(r.Context(), itemID)
		if err != nil {
			http.Error(w, "failed to load customization options", http.StatusInternalServerError)
			return
		}

		var selected []data.SelectedModifier
		for _, g := range groups {
			chosen := r.Form["mod_"+g.ID]
			if len(chosen) < g.MinSelect || len(chosen) > g.MaxSelect {
				toast(fmt.Sprintf("%s: choose between %d and %d", g.Name, g.MinSelect, g.MaxSelect))
				return
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
					toast("Invalid customization selected")
					return
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
