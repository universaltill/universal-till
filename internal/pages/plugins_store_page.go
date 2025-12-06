package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// PluginStoreHandler handles the marketplace plugin store page
func PluginStoreHandler(deps *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check if marketplace is configured
		if deps.CatalogRepo == nil {
			http.Error(w, "Marketplace not configured", http.StatusServiceUnavailable)
			return
		}

		// LOCAL-FIRST: Get catalog from cache only, don't auto-fetch
		snapshot, isStale, err := deps.CatalogRepo.Get()
		if err != nil {
			// No cache available - show empty state, user must click "Sync" button
			data := map[string]interface{}{
				"title":     "Plugin Store",
				"theme":     deps.State.Theme,
				"menuItems": deps.Menu,
				"Plugins":   []interface{}{},
				"IsStale":   false,
				"NoCache":   true, // Flag to show "Sync Now" button
				"Filters": map[string]string{
					"Type":      "",
					"Developer": "",
					"TrustTier": "",
				},
			}
			httpx.Render("ui/pages/plugins_store.html", data)(w, r)
			return
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
			"title":     "Plugin Store",
			"theme":     deps.State.Theme,
			"menuItems": deps.Menu,
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

		// Force fetch fresh catalog (use configured locale)
		locale := deps.Cfg.DefaultLocale
		if locale == "" {
			locale = "en-US" // fallback
		}
		snapshot, err := deps.CatalogRepo.Fetch(ctx, locale, "linux/amd64")
		if err != nil {
			http.Error(w, "Failed to refresh catalog", http.StatusInternalServerError)
			return
		}

		// Apply filters from query params (same as main handler)
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

		// Render the plugin grid HTML directly
		w.Header().Set("Content-Type", "text/html")
		if len(plugins) == 0 {
			w.Write([]byte(`<div class="text-center py-12">
				<h3 class="text-lg font-medium text-gray-900">No plugins found</h3>
				<p class="mt-1 text-sm text-gray-500">Try adjusting your filters.</p>
			</div>`))
			return
		}

		// Build the plugin grid HTML
		html := `<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">`
		for _, plugin := range plugins {
			trustBadge := ""
			switch plugin.TrustTier {
			case "verified":
				trustBadge = `<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">✓ Verified</span>`
			case "approved":
				trustBadge = `<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-blue-100 text-blue-800">Approved</span>`
			default:
				trustBadge = `<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-yellow-100 text-yellow-800">Community</span>`
			}

			html += `<div class="bg-white shadow rounded-lg p-6 hover:shadow-xl transition-shadow">
				<div class="flex items-start justify-between mb-4">
					<div>
						<h3 class="text-xl font-semibold text-gray-900">` + plugin.Name + `</h3>
						<p class="text-sm text-gray-500">v` + plugin.Version + ` by ` + plugin.DeveloperID + `</p>
					</div>
					` + trustBadge + `
				</div>
				<p class="text-gray-600 text-sm mb-4">` + plugin.Description + `</p>
				<div class="flex items-center justify-between text-xs text-gray-500 mb-4">
					<span>Type: ` + plugin.CanonicalType + `</span>
					<span>` + plugin.DeviceArch + `</span>
				</div>
				<button 
					onclick="installPlugin('` + plugin.ListingID + `', '` + plugin.Name + `')"
					class="w-full bg-blue-500 hover:bg-blue-700 text-white font-bold py-2 px-4 rounded transition-colors">
					Install
				</button>
			</div>`
		}
		html += `</div>`

		w.Write([]byte(html))
	}
}

// registerPluginStore registers the plugin store routes
func registerPluginStore(mux *http.ServeMux, deps *common.Deps) {
	mux.HandleFunc("/plugins/store", PluginStoreHandler(deps))
	mux.HandleFunc("/plugins/store/refresh", PluginStoreRefreshHandler(deps))
}
