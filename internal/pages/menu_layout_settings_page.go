package pages

import (
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/uislot"
)

// Settings → Hidden menu tiles: ADR-0088 Decision D's findability surface.
// A `layout` plugin may hide a Menu tile (never a route), and the risk is a
// merchant who can never find that destination again — so this page lists
// every destination a layout plugin currently hides, names the plugin that
// hid each one, links straight to it, and restores it per entry without
// uninstalling the plugin. A hide mechanism without this page is not
// shippable.
//
// Same gate as every other settings surface: canPerform(d, r, "settings")
// (ut-docs#901/#902's lesson — not a raw session check).

// hiddenMenuRow is one hidden destination as the page shows it.
type hiddenMenuRow struct {
	Key      string
	Href     string
	LabelKey string   // the CORE label — what the tile would say
	Plugins  []string // display names of every plugin hiding it (hide is idempotent, Decision F)
	Restored bool     // the merchant restored it; the plugin still declares the hide
}

// hiddenMenuRows lists every core key some active layout plugin hides,
// restores included (shown as restored, with the way back), in the order
// the amendments were loaded — plugin install order, then declaration.
func hiddenMenuRows(r *http.Request, d *common.Deps) []hiddenMenuRow {
	restored := common.RestoredMenuKeys(r.Context(), d.Settings)
	var rows []hiddenMenuRow
	index := map[string]int{}
	for _, a := range d.LayoutAmendmentsSnapshot() {
		// LayoutAmendmentsSnapshot is every active plugin's amendments of
		// EVERY registered slot (ut-docs#1911), not Menu-only any more —
		// this page is the Menu slot's own findability surface, so it must
		// filter to its own slot the same way common.BuildMenuAmendments
		// does. Currently unreachable in practice (the Items slot refuses
		// `hide` outright — see uislot's itemsSpec), but that's the OTHER
		// half of the guarantee, not a reason to skip this one (independent
		// review of ut-docs#1911, finding 3).
		if a.Slot != uislot.MenuSlot {
			continue
		}
		if !a.Hide {
			continue
		}
		name := a.PluginID
		if p, ok := d.InstalledPlugin(a.PluginID); ok && strings.TrimSpace(p.Name) != "" {
			name = p.Name
		}
		if i, seen := index[a.Key]; seen {
			rows[i].Plugins = append(rows[i].Plugins, name)
			continue
		}
		e, ok := uislot.CoreMenuEntry(a.Key)
		if !ok {
			continue // cannot happen post-validation; never show a row for a key that has no tile
		}
		index[a.Key] = len(rows)
		rows = append(rows, hiddenMenuRow{Key: a.Key, Href: e.Href, LabelKey: e.LabelKey, Plugins: []string{name}, Restored: restored[a.Key]})
	}
	return rows
}

// amendedMenuRow is one non-hide change a layout plugin made to a Menu
// destination that STAYS visible — the "what changed" half of Decision D's
// findability surface (ut-docs#1904 review finding F1, this card). A tile a
// plugin re-labels, re-icons, reorders or re-groups leaves no trace on the
// hides-only page above; this is where a merchant sees it.
type amendedMenuRow struct {
	Key      string
	Href     string
	LabelKey string // the CURRENT label (possibly plugin-set), resolved through T
	Plugin   string // display name of the plugin that made the change

	// CoreLabelKey/CoreIcon are set (non-empty) only when that field
	// actually changed — the core default the amendment replaced. Empty
	// means "unchanged", never "core default is the empty string" (every
	// CoreMenu entry declares both).
	CoreLabelKey string
	CoreIcon     string
	Icon         string // the CURRENT icon name; only meaningful if CoreIcon != ""

	Reordered bool
	Regrouped bool
	// CoreGroup/Group are the before/after Group locale keys, only
	// meaningful when Regrouped — "" means ungrouped (the template shows
	// its own "ungrouped" copy rather than resolving T "").
	CoreGroup string
	Group     string
}

// amendedMenuRows lists every core key some active layout plugin restructures
// (re-label/re-icon/reorder/re-group) without hiding it, naming the plugin
// and the core default each changed field replaced. Restructure amendments
// on the same key across two different plugins are refused at install
// (internal/plugins/layout_validation_test.go), so unlike hiddenMenuRows
// above there is at most one plugin per key here — no aggregation needed.
//
// A hide by one plugin and a restructure by a DIFFERENT plugin on the SAME
// key is NOT refused at install (that validation only checks
// restructure-vs-restructure — a hide candidate never reaches it), and
// uislot.Resolve makes hide win over any restructure on the same key
// regardless of amendment order. Showing such a key here anyway would tell
// the merchant it "stays on the Menu" (this section's own intro copy) while
// hiddenMenuRows is, correctly, ALSO showing it as hidden — a direct
// contradiction on the same page. So skip any key some amendment hides,
// even one belonging to a different plugin than the restructure under
// consideration (independent review finding, ut-docs#1921).
func amendedMenuRows(d *common.Deps) []amendedMenuRow {
	amendments := d.LayoutAmendmentsSnapshot()
	hiddenKeys := map[string]bool{}
	for _, a := range amendments {
		if a.Hide {
			hiddenKeys[a.Key] = true
		}
	}
	var rows []amendedMenuRow
	for _, a := range amendments {
		if a.Hide || !a.Restructures() {
			continue // hides are hiddenMenuRows' job; every non-hide amendment restructures (parseAmendment refuses a no-op)
		}
		if hiddenKeys[a.Key] {
			continue // a different plugin hides this key — hide wins at render, hiddenMenuRows already lists it
		}
		core, ok := uislot.CoreMenuEntry(a.Key)
		if !ok {
			continue // cannot happen post-validation; mirrors hiddenMenuRows
		}
		name := a.PluginID
		if p, ok := d.InstalledPlugin(a.PluginID); ok && strings.TrimSpace(p.Name) != "" {
			name = p.Name
		}
		// Reuse uislot.Resolve itself for the label/icon before-value —
		// LabelFallback/IconFallback are exactly "the core default this
		// amendment replaced" (see slot.go's Resolve), so this page shows
		// the same fallback the render-time menu already computes rather
		// than re-deriving it.
		resolved := uislot.Resolve([]uislot.Entry{core}, []uislot.Amendment{a})[0]
		row := amendedMenuRow{Key: a.Key, Href: core.Href, LabelKey: resolved.LabelKey, Plugin: name}
		if resolved.LabelFallback != "" {
			row.CoreLabelKey = resolved.LabelFallback
		}
		if resolved.IconFallback != "" {
			row.CoreIcon = resolved.IconFallback
			row.Icon = resolved.Icon
		}
		if a.Order != nil {
			row.Reordered = true
		}
		if a.Group != "" {
			row.Regrouped = true
			row.CoreGroup = core.Group
			row.Group = a.Group
		}
		rows = append(rows, row)
	}
	return rows
}

// menuLayoutErrorKeys is the allowlist of ?err= values this page renders —
// through T, so anything else in the query string is dropped rather than
// echoed back as text.
var menuLayoutErrorKeys = map[string]bool{
	"menulayout.error.unknown_key": true,
}

func registerMenuLayoutSettings(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /settings/menu", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return
		}
		errKey := r.URL.Query().Get("err")
		if !menuLayoutErrorKeys[errKey] {
			errKey = ""
		}
		httpx.Render("ui/pages/menu_layout.html", map[string]any{
			"title":   "Hidden menu tiles",
			"theme":   d.CurrentState().Theme,
			"rows":    hiddenMenuRows(r, d),
			"amended": amendedMenuRows(d),
			"errKey":  errKey,
		})(w, r)
	})

	// setRestored is both POST handlers: restore puts a hidden tile back on
	// the Menu, rehide undoes that. The key must be one a plugin actually
	// hides — anything else (a typo, a tile nothing hides) is refused with
	// an error the page shows, never a silent write of a meaningless key.
	setRestored := func(restore bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !canPerform(d, r, "settings") {
				httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
				return
			}
			key := strings.TrimSpace(r.FormValue("key"))
			hidden := false
			for _, a := range d.LayoutAmendmentsSnapshot() {
				// Same Menu-slot filter as hiddenMenuRows above, and for
				// the same reason: this page/form only ever means the Menu
				// slot, and the amendment pool is no longer Menu-only.
				if a.Slot != uislot.MenuSlot {
					continue
				}
				if a.Hide && a.Key == key {
					hidden = true
					break
				}
			}
			if !hidden {
				http.Redirect(w, r, "/settings/menu?err=menulayout.error.unknown_key", http.StatusSeeOther)
				return
			}
			ctx := r.Context()
			restored := common.RestoredMenuKeys(ctx, d.Settings)
			if restore {
				restored[key] = true
			} else {
				delete(restored, key)
			}
			if err := common.SaveRestoredMenuKeys(ctx, d.Settings, restored); err != nil {
				logging.L().Errorf("menu layout: save restored keys: %v", err)
				httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
				return
			}
			d.ReloadMenuAmendments(ctx)
			http.Redirect(w, r, "/settings/menu", http.StatusSeeOther)
		}
	}
	mux.HandleFunc("POST /api/settings/menu/restore", setRestored(true))
	mux.HandleFunc("POST /api/settings/menu/rehide", setRestored(false))
}
