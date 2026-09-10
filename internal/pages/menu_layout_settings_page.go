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
			"title":  "Hidden menu tiles",
			"theme":  d.CurrentState().Theme,
			"rows":   hiddenMenuRows(r, d),
			"errKey": errKey,
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
