package pages

import (
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"strconv"

	"github.com/universaltill/universal-till/internal/httpx"
)

func registerPluginsPage(mux *http.ServeMux, d *deps) {
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
		offset := (page - 1) * size
		items, total, err := d.pm.CatalogPage(r.Context(), offset, size, tag)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// collect all tags from full catalog for tabs
		tagsSet := make(map[string]struct{})
		for _, c := range d.pm.Catalog {
			for _, t := range c.Tags {
				tagsSet[t] = struct{}{}
			}
		}
		var tags []string
		for t := range tagsSet {
			tags = append(tags, t)
		}
		sort.Strings(tags)
		payload := map[string]any{
			"items":    items,
			"total":    total,
			"page":     page,
			"pageSize": size,
			"type":     tag,
			"tags":     tags,
		}
		raw, _ := json.Marshal(payload)
		data := map[string]any{
			"title":       "Plugins",
			"theme":       d.state.Theme,
			"menuItems":   d.menu,
			"pluginsJSON": template.JS(raw),
		}
		httpx.Render("ui/pages/plugins.html", data)(w, r)
	})
}
