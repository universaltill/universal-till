package pages

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// registerPluginsPage renders the installed-plugins MANAGER: every plugin on
// the till (enabled or disabled) with lifecycle actions. Discovering,
// downloading and installing new plugins happens on /plugins/store.
func registerPluginsPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/plugins", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		rows, err := data.NewPluginRepo(d.Db).ListManagedPlugins(ctx)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		statuses, _ := plugins.NewInstallStatusStore(d.Db).List(ctx)

		// Docs button targets (ADR-0037): a plugin registering a page entry
		// under the reserved key "docs" gets a Docs button pointing at that
		// entry's route. ListPageEntries already filters to active entries of
		// active plugins, so a disabled plugin (or entry) surfaces no route —
		// never a button that opens an empty/404 page.
		docsRouteByPlugin := map[string]string{}
		if entries, err := data.NewPluginRepo(d.Db).ListPageEntries(ctx); err == nil {
			for _, e := range entries {
				if e.EntryKey == plugins.DocsEntryKey && e.Route != "" {
					if _, seen := docsRouteByPlugin[e.PluginID]; !seen {
						docsRouteByPlugin[e.PluginID] = e.Route
					}
				}
			}
		}

		// Catalog match per installed plugin, via the same two-tier
		// byListing/byAuthorName resolution UpdateChecker already uses
		// (plugins.IndexCatalog/Resolve, ut-docs#2131) — byListing alone left
		// every file-imported plugin (no plugin_install_status row at all,
		// internal/data/sync_plugins_repo.go) permanently unable to report an
		// update, indistinguishable from "you are current". GetOrFetch serves
		// the cache when fresh and refreshes it when stale (offline-first
		// fallback preserved on a failed refetch) rather than serving an
		// arbitrarily old cached snapshot forever.
		listingByPlugin := map[string]string{}
		for listingID, st := range statuses {
			if st.PluginID != "" {
				listingByPlugin[st.PluginID] = listingID
			}
		}
		var catalogIdx plugins.CatalogIndex
		if d.CatalogRepo != nil {
			if snapshot, _, err := d.CatalogRepo.GetOrFetch(r.Context(), httpx.ResolveLocale(w, r), ""); err == nil && snapshot != nil {
				catalogIdx = plugins.IndexCatalog(snapshot)
			}
		}

		search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		items := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			if search != "" && !strings.Contains(strings.ToLower(row.Name), search) &&
				!strings.Contains(strings.ToLower(row.ID), search) {
				continue
			}
			var latest string
			var hasUpdate, versionUnknown bool
			// versionUnknown covers TWO cases, not just "no match at all"
			// (ut-docs#2131 review): a resolved catalog entry with an empty
			// Version is just as much an unknown as no entry at all — AC2
			// says latest:"" must never be presented as current, and a
			// match with an empty version falls straight through that gap
			// otherwise (hasUpdate stays false, versionUnknown would stay
			// false too, rendering identically to "you are current").
			if catalogPlugin, ok := catalogIdx.Resolve(listingByPlugin[row.ID], row.Author, row.Name); ok && catalogPlugin.Version != "" {
				latest = catalogPlugin.Version
				hasUpdate = plugins.VersionNewer(latest, row.Version)
			} else {
				versionUnknown = true
			}
			items = append(items, map[string]any{
				"id":             row.ID,
				"name":           row.Name,
				"version":        row.Version,
				"enabled":        row.IsActive,
				"trust":          row.TrustLevel,
				"state":          row.InstallState,
				"hasUpdate":      hasUpdate,
				"latest":         latest,
				"versionUnknown": versionUnknown,
				"docsRoute":      docsRouteByPlugin[row.ID],
			})
		}

		raw, _ := json.Marshal(map[string]any{"items": items, "q": search})
		httpx.Render("ui/pages/plugins.html", map[string]any{
			"title":       "Plugins",
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.MenuSnapshot(),
			"pluginsJSON": template.JS(raw),
		})(w, r)
	})
}
