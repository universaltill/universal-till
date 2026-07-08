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
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
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

		// Latest catalog versions (cache only — the manager page must work
		// offline) keyed by plugin id via the install-status listing mapping.
		latestByPlugin := map[string]string{}
		if d.CatalogRepo != nil {
			if snapshot, _, err := d.CatalogRepo.Get(); err == nil && snapshot != nil {
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
			})
		}

		raw, _ := json.Marshal(map[string]any{"items": items, "q": search})
		httpx.Render("ui/pages/plugins.html", map[string]any{
			"title":       "Plugins",
			"theme":       d.State.Theme,
			"menuItems":   d.Menu,
			"pluginsJSON": template.JS(raw),
		})(w, r)
	})
}

// installedPluginForSummary maps a marketplace catalog summary to an installed
// plugin (kept for the store/status pages).
func installedPluginForSummary(pm *plugins.Manager, summary marketplace.PluginSummary) (plugins.Plugin, bool) {
	if pm == nil {
		return plugins.Plugin{}, false
	}
	if listingID := strings.TrimSpace(summary.ListingID); listingID != "" {
		if inst, exists := pm.Installed[listingID]; exists {
			return inst, true
		}
	}
	if pluginID := strings.TrimSpace(summary.ID); pluginID != "" {
		if inst, exists := pm.Installed[pluginID]; exists {
			return inst, true
		}
	}
	return plugins.Plugin{}, false
}
