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
// non-nil for an unexpected/server error (500). engine is the caller's own
// basket engine (d.Engine for the cashier, d.KioskEngine for the kiosk,
// ut-docs#449) — ResolveBase is read-only and both engines share one
// resolver instance, so this is a consistency fix (every kiosk call now
// goes through KioskEngine, matching the rest of the split) rather than a
// behavior change.
func resolveAndValidateModifiers(ctx context.Context, d *common.Deps, engine *pos.Service, locale, code, itemID string, form url.Values) (pos.BasketLine, []data.SelectedModifier, string, error) {
	base, ok := engine.ResolveBase(code)
	if !ok {
		return pos.BasketLine{}, nil, "Item not found", errors.New("resolve base: not found")
	}
	if itemID == "" || base.ItemID != itemID {
		// code and itemId are two independent caller-supplied values (the
		// picker's hidden inputs); a manipulated or stale submission could
		// send a mismatched pair. See docs/code-reviews/2026-07-24-item-modifiers-cashier-ui.md.
		return pos.BasketLine{}, nil, "Item not found", errors.New("code/itemId mismatch")
	}

	catalogRepo := data.NewCatalogRepo(d.Db)
	variants, err := catalogRepo.ItemVariantsFor(ctx, itemID)
	if err != nil {
		return pos.BasketLine{}, nil, "", fmt.Errorf("load item variants: %w", err)
	}
	variants = sellableVariants(variants)

	// ut-docs#2209: an item with at least one SELLABLE variant (small/
	// regular/large, ...) must never be added at its own parent base
	// price — the whole point of this card. base gets swapped for the
	// chosen variant's own resolved line below.
	submittedVariantID := strings.TrimSpace(form.Get("variantId"))

	// DO NOT REMOVE (ut-docs#2209 review, blocker 1). len(variants) is read
	// at SUBMIT time, but the picker rendered earlier — so the item's last
	// sellable variant can be deactivated while the modal sits open. Without
	// this, that submission skips the whole variant branch and silently adds
	// the PARENT base line: a 9.99 line for a 3.10 coffee, no error, no log —
	// precisely the money defect this card exists to remove, now laundered
	// through a dialog that appears to have asked the question. A submitted
	// variantId with nothing to match it against is a conflict, never a
	// fallback.
	if len(variants) == 0 && submittedVariantID != "" {
		return pos.BasketLine{}, nil, httpx.T(locale, "modifiers.variant_unavailable"), errors.New("variantId submitted but item has no sellable variants (deactivated mid-pick?)")
	}

	if len(variants) > 0 {
		variantID := submittedVariantID
		if variantID == "" {
			return pos.BasketLine{}, nil, httpx.T(locale, "modifiers.variant_required"), errors.New("variant required, none submitted")
		}

		// DO NOT REMOVE: this membership check is the SOLE guard against
		// selling one item's variant priced/labeled as a DIFFERENT item.
		// itemId and variantId are two independent caller-supplied values
		// (the picker's hidden input and its radio group) — exactly the
		// same shape as the code/itemId pair checked above — so a
		// manipulated or stale submission could pair THIS item's itemId
		// with a variantId that actually belongs to a different item. The
		// only safe check is membership in THIS item's own server-loaded,
		// sellable-variant set (never a bare "does this variant id exist
		// anywhere" lookup, which would accept any real variant id
		// regardless of which item it belongs to).
		var match *data.VariantView
		for i := range variants {
			if variants[i].ID == variantID {
				match = &variants[i]
				break
			}
		}
		if match == nil {
			// NOT "item not found": the item resolved fine — it is the
			// chosen variant that does not belong to it (or was just
			// retired). Pointing the operator at the item sends them
			// looking in the wrong place.
			return pos.BasketLine{}, nil, httpx.T(locale, "modifiers.variant_unavailable"), errors.New("variant id not a member of item's sellable variants")
		}

		label, ok, err := catalogRepo.GetVariantLabel(ctx, variantID)
		if err != nil {
			return pos.BasketLine{}, nil, "", fmt.Errorf("load variant label %s: %w", variantID, err)
		}
		if !ok || label.Code == "" {
			// sellableVariants already excludes a codeless variant, and
			// match came from that same filtered set, so this is a
			// server-side inconsistency (GetVariantLabel disagreeing with
			// ItemVariantsFor about the resolvable code), not a user
			// mistake. NOTE: this branch does NOT catch deactivation —
			// ItemVariantsFor filters is_active = 1, so a variant retired
			// mid-pick disappears from `variants` and is caught by the
			// membership check above, or by the len(variants) == 0 guard
			// before it. An earlier version of this comment claimed
			// otherwise (ut-docs#2209 review).
			return pos.BasketLine{}, nil, "", fmt.Errorf("resolve variant %s: no resolvable code (ok=%v)", variantID, ok)
		}
		variantBase, ok := engine.ResolveBase(label.Code)
		if !ok {
			return pos.BasketLine{}, nil, "", fmt.Errorf("resolve variant %s by code %q: not found", variantID, label.Code)
		}
		if variantBase.VariantID != variantID {
			// Never a silent pass: the resolved line MUST carry the exact
			// variant the operator picked, or a resolver bug (e.g. a
			// duplicate/ambiguous code) could silently substitute a
			// different variant's price.
			return pos.BasketLine{}, nil, "", fmt.Errorf("resolved variant line carries VariantID %q, want %q", variantBase.VariantID, variantID)
		}
		base = variantBase
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

// sellableVariants filters out any variant with no resolvable code — no
// barcode AND no SKU (ut-docs#2209). This product's standing rule is that
// a codeless thing never reaches the sale screen (ut-docs#1459); a variant
// with nothing to scan/resolve it by can never actually be sold, so
// offering it in the picker would let an operator choose a size that can
// never be added to the basket. If this filters out every variant an item
// has, that item behaves exactly as if it had none.
func sellableVariants(variants []data.VariantView) []data.VariantView {
	out := make([]data.VariantView, 0, len(variants))
	for _, v := range variants {
		if v.Barcode != "" || v.SKU != "" {
			out = append(out, v)
		}
	}
	return out
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
	catalogRepo := data.NewCatalogRepo(d.Db)

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
		variants, err := catalogRepo.ItemVariantsFor(r.Context(), itemID)
		if err != nil {
			http.Error(w, "failed to load customization options", http.StatusInternalServerError)
			return
		}

		httpx.RenderPartial("ui/partials/modifier_picker.html", map[string]any{
			"ItemID":   itemID,
			"Code":     code,
			"ItemName": base.Name,
			"Groups":   groups,
			"Variants": sellableVariants(variants),
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
			b.ToastLevel = "error"
			_ = basketView.Render(w, &b)
		}

		base, selected, userMsg, err := resolveAndValidateModifiers(r.Context(), d, d.Engine, httpx.ResolveLocale(w, r), code, itemID, r.Form)
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
