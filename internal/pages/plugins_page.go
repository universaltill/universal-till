package pages

import (
	"encoding/json"
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

		// LOCAL-FIRST: Use cached catalog if available
		var items []map[string]interface{}
		var tagsSet = make(map[string]struct{})

		if d.CatalogRepo != nil {
			// Try to get from cache first
			snapshot, _, err := d.CatalogRepo.Get()
			if err == nil && snapshot != nil {
				// Transform catalog plugins to format expected by UI
				for _, p := range snapshot.Plugins {
					// Check if installed
					installed := false
					enabled := false
					currentVersion := ""
					hasUpdate := false
					
					if inst, exists := d.Pm.Installed[p.ListingID]; exists {
						installed = true
						enabled = inst.IsActive
						currentVersion = inst.Version
						
						// Check if update available (simple string comparison - production should use semantic versioning)
						if p.Version != currentVersion {
							hasUpdate = true
						}
					}

					items = append(items, map[string]interface{}{
						"id":             p.ListingID,
						"name":           p.Name,
						"version":        p.Version,
						"currentVersion": currentVersion,
						"description":    p.Description,
						"author":         p.DeveloperID,
						"packageUrl":     p.ArtifactURL,
						"sha256":         p.ArtifactHash,
						"tags":           []string{p.CanonicalType},
						"installed":      installed,
						"enabled":        enabled,
						"hasUpdate":      hasUpdate,
					})

					// Collect tags
					if p.CanonicalType != "" {
						tagsSet[p.CanonicalType] = struct{}{}
					}
				}
			}
		}
		var tags []string
		for t := range tagsSet {
			tags = append(tags, t)
		}
		sort.Strings(tags)

		// Apply type filter if specified
		filteredItems := items
		if tag != "" {
			filteredItems = make([]map[string]interface{}, 0)
			for _, item := range items {
				itemTags, ok := item["tags"].([]string)
				if ok {
					for _, t := range itemTags {
						if t == tag {
							filteredItems = append(filteredItems, item)
							break
						}
					}
				}
			}
		}

		offset := (page - 1) * size
		total := len(filteredItems)

		// Apply client-side pagination
		start := offset
		end := offset + size
		if start > len(filteredItems) {
			start = len(filteredItems)
		}
		if end > len(filteredItems) {
			end = len(filteredItems)
		}
		paged := filteredItems[start:end]

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
