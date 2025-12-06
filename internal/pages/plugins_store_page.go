package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// PluginStoreHandler handles the marketplace plugin store page
func PluginStoreHandler(deps *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Check if marketplace is configured
		if deps.CatalogRepo == nil {
			http.Error(w, "Marketplace not configured", http.StatusServiceUnavailable)
			return
		}

		// Get catalog from repository
		snapshot, isStale, err := deps.CatalogRepo.Get()
		if err != nil {
			// Try to fetch if no cache available
			snapshot, err = deps.CatalogRepo.Fetch(ctx, "en", "linux/amd64")
			if err != nil {
				http.Error(w, "Failed to load plugin catalog: "+err.Error(), http.StatusInternalServerError)
				return
			}
			isStale = false
		}

		// Apply filters from query params
		pluginType := r.URL.Query().Get("type")
		developer := r.URL.Query().Get("developer")
		trustTier := r.URL.Query().Get("trust_tier")

		plugins := snapshot.Plugins
		if pluginType != "" || developer != "" || trustTier != "" {
			filtered, err := deps.CatalogRepo.Filter(pluginType, developer, trustTier)
			if err != nil {
				http.Error(w, "Failed to filter plugins", http.StatusInternalServerError)
				return
			}
			plugins = filtered
		}

		data := map[string]interface{}{
			"Title":     "Plugin Store",
			"Theme":     deps.State.Theme,
			"MenuItems": deps.Menu,
			"Plugins":   plugins,
			"IsStale":   isStale,
			"FetchedAt": snapshot.FetchedAt,
			"Filters": map[string]string{
				"Type":      pluginType,
				"Developer": developer,
				"TrustTier": trustTier,
			},
		}

		httpx.Render("ui/pages/plugins_store.html", data)(w, r)
	}
}

// PluginStoreRefreshHandler handles HTMX refresh requests
func PluginStoreRefreshHandler(deps *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if deps.CatalogRepo == nil {
			http.Error(w, "Marketplace not configured", http.StatusServiceUnavailable)
			return
		}

		// Force fetch fresh catalog
		snapshot, err := deps.CatalogRepo.Fetch(ctx, "en", "linux/amd64")
		if err != nil {
			http.Error(w, "Failed to refresh catalog", http.StatusInternalServerError)
			return
		}

		// Return just the plugin list partial
		data := map[string]interface{}{
			"Plugins": snapshot.Plugins,
			"IsStale": false,
		}

		httpx.Render("ui/partials/plugin_list.html", data)(w, r)
	}
}

// registerPluginStore registers the plugin store routes
func registerPluginStore(mux *http.ServeMux, deps *common.Deps) {
	mux.HandleFunc("/plugins/store", PluginStoreHandler(deps))
	mux.HandleFunc("/plugins/store/refresh", PluginStoreRefreshHandler(deps))
}
