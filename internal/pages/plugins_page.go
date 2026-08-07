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
			http.Error(w, "failed to load plugins", http.StatusInternalServerError)
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
				if e.EntryKey == "docs" && e.Route != "" {
					if _, seen := docsRouteByPlugin[e.PluginID]; !seen {
						docsRouteByPlugin[e.PluginID] = e.Route
					}
				}
			}
		}

		// Latest catalog versions keyed by plugin id via the install-status
		// listing mapping. GetOrFetch serves the cache when present (offline-
		// first) and only fetches when there is no cache at all — otherwise a
		// fresh boot never shows "update available" until the store is visited.
		latestByPlugin := map[string]string{}
		if d.CatalogRepo != nil {
			if snapshot, _, err := d.CatalogRepo.GetOrFetch(r.Context(), httpx.ResolveLocale(w, r), ""); err == nil && snapshot != nil {
				latestByListing := map[string]string{}
				for _, p := range snapshot.Plugins {
					id := p.ListingID
					if id == "" {
						id = p.ID
					}
					latestByListing[id] = p.Version
				}
				for listingID, st := range statuses {
					if v, ok := latestByListing[listingID]; ok && st.PluginID != "" {
						latestByPlugin[st.PluginID] = v
					}
				}
			}
		}

		search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		items := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			if search != "" && !strings.Contains(strings.ToLower(row.Name), search) &&
				!strings.Contains(strings.ToLower(row.ID), search) {
				continue
			}
			latest := latestByPlugin[row.ID]
			items = append(items, map[string]any{
				"id":        row.ID,
				"name":      row.Name,
				"version":   row.Version,
				"enabled":   row.IsActive,
				"trust":     row.TrustLevel,
				"state":     row.InstallState,
				"hasUpdate": latest != "" && latest != row.Version,
				"latest":    latest,
				"docsRoute": docsRouteByPlugin[row.ID],
			})
		}

		raw, _ := json.Marshal(map[string]any{"items": items, "q": search})
		httpx.Render("ui/pages/plugins.html", map[string]any{
			"title":       "Plugins",
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.Menu,
			"pluginsJSON": template.JS(raw),
		})(w, r)
	})
}
