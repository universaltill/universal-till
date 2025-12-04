package pages

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerPluginsPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/plugins", func(w http.ResponseWriter, r *http.Request) {
		page := 1
		size := 12
		tag := r.URL.Query().Get("type")
		if v := r.URL.Query().Get("page"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				page = n
			}
		}
		if v := r.URL.Query().Get("size"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				size = n
			}
		}

		// Fetch plugins from marketplace API instead of local catalog
		marketplaceURL := d.Cfg.MarketplaceURL + "/v1/catalog/plugins"
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, marketplaceURL, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if tag != "" {
			q := req.URL.Query()
			q.Set("capability", tag)
			req.URL.RawQuery = q.Encode()
		}

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf("marketplace request failed: %v", err), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			http.Error(w, fmt.Sprintf("marketplace returned status %d", resp.StatusCode), resp.StatusCode)
			return
		}

		// Parse marketplace response
		var marketplaceResp struct {
			Plugins         []map[string]interface{} `json:"plugins"`
			NextPageToken   string                   `json:"next_page_token"`
			SnapshotVersion int64                    `json:"snapshot_version"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&marketplaceResp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Transform marketplace plugins to catalog format expected by UI
		items := make([]map[string]interface{}, len(marketplaceResp.Plugins))
		for i, p := range marketplaceResp.Plugins {
			// Check if installed
			installed := false
			if pluginID, ok := p["id"].(string); ok {
				if _, exists := d.Pm.Installed[pluginID]; exists {
					installed = true
				}
			}

			items[i] = map[string]interface{}{
				"id":          p["id"],
				"name":        p["name"],
				"version":     p["version"],
				"description": p["description"],
				"author":      p["vendor"], // Map vendor to author for UI
				"packageUrl":  p["package_url"],
				"sha256":      p["sha256"],
				"tags":        safeGetTags(p["type"]),
				"installed":   installed,
			}
		}

		// Collect tags from plugins
		tagsSet := make(map[string]struct{})
		for _, p := range marketplaceResp.Plugins {
			if pluginType, ok := p["type"].(string); ok {
				tagsSet[pluginType] = struct{}{}
			}
		}
		var tags []string
		for t := range tagsSet {
			tags = append(tags, t)
		}
		sort.Strings(tags)

		offset := (page - 1) * size
		total := len(items)

		// Apply client-side pagination
		start := offset
		end := offset + size
		if start > len(items) {
			start = len(items)
		}
		if end > len(items) {
			end = len(items)
		}
		paged := items[start:end]

		payload := map[string]any{
			"items":    paged,
			"total":    total,
			"page":     page,
			"pageSize": size,
			"type":     tag,
			"tags":     tags,
		}
		raw, _ := json.Marshal(payload)
		data := map[string]any{
			"title":       "Plugins",
			"theme":       d.State.Theme,
			"menuItems":   d.Menu,
			"pluginsJSON": template.JS(raw),
		}
		httpx.Render("ui/pages/plugins.html", data)(w, r)
	})
}

// safeGetTags safely extracts plugin type as a tag, handling type assertion failures
func safeGetTags(typeVal interface{}) []string {
	if typeStr, ok := typeVal.(string); ok && typeStr != "" {
		return []string{typeStr}
	}
	return []string{}
}
