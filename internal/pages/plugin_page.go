package pages

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Overridable in tests.
var pluginPagesDir = "./data/plugins"

// contentBundle is the localized plugin content contract: a plugin page ships
// content/<locale>.json files carrying categorized entries (the format the
// FAQ reference plugin publishes).
type contentBundle struct {
	Locale         string `json:"locale"`
	RTL            bool   `json:"rtl"`
	FallbackLocale string `json:"fallback_locale"`
	Categories     []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		SortOrder int    `json:"sort_order"`
	} `json:"categories"`
	Entries []struct {
		ID        string `json:"id"`
		Category  string `json:"category"`
		Question  string `json:"question"`
		Answer    string `json:"answer"`
		SortOrder int    `json:"sort_order"`
	} `json:"faq_entries"`
}

// registerPluginPages dispatches plugin-provided UI:
//   - GET /plugin/...              -> the page a 'page' entry registered at that route
//   - GET /ui/plugin-buttons       -> partial listing installed 'button' entries
//   - POST /api/plugins/entries/{plugin}/{key}/action -> publish the button's event
func registerPluginPages(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/plugin/", func(w http.ResponseWriter, r *http.Request) {
		entry, ok := findPageEntry(r, d)
		if !ok {
			http.NotFound(w, r)
			return
		}
		renderPluginPage(w, r, d, entry)
	})

	mux.HandleFunc("GET /ui/plugin-buttons", func(w http.ResponseWriter, r *http.Request) {
		buttons, _ := data.NewPluginRepo(d.Db).ListButtonEntries(r.Context())
		httpx.RenderPartial("ui/partials/plugin_buttons.html", map[string]any{
			"Buttons": buttons,
		})(w, r)
	})

	mux.HandleFunc("POST /api/plugins/entries/{plugin}/{key}/action", func(w http.ResponseWriter, r *http.Request) {
		pluginID, key := r.PathValue("plugin"), r.PathValue("key")
		buttons, err := data.NewPluginRepo(d.Db).ListButtonEntries(r.Context())
		if err != nil {
			http.Error(w, "failed to load plugin buttons", http.StatusInternalServerError)
			return
		}
		for _, b := range buttons {
			if b.PluginID != pluginID || b.EntryKey != key {
				continue
			}
			// The button's action is an event: hooks/processes subscribed to it
			// react; the publish is audited either way.
			eventType := b.TriggerEvent
			if eventType == "" {
				eventType = b.TargetAction
			}
			if eventType == "" {
				eventType = "plugin.button.pressed"
			}
			eventID, err := plugins.SharedBus(d.Db).Publish(r.Context(), eventType, map[string]any{
				"plugin_id": b.PluginID,
				"entry_key": b.EntryKey,
				"label":     b.Label,
			})
			if err != nil {
				http.Error(w, fmt.Sprintf("action failed: %v", err), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"success": true, "event": eventType, "event_id": eventID,
			})
			return
		}
		http.NotFound(w, r)
	})
}

// findPageEntry resolves the active plugin page entry registered at the
// request path.
func findPageEntry(r *http.Request, d *common.Deps) (data.PageEntryRow, bool) {
	entries, err := data.NewPluginRepo(d.Db).ListPageEntries(r.Context())
	if err != nil {
		return data.PageEntryRow{}, false
	}
	for _, e := range entries {
		if e.Route != "" && e.Route == r.URL.Path {
			return e, true
		}
	}
	return data.PageEntryRow{}, false
}

// renderPluginPage renders a plugin page inside the POS chrome. Content
// sources, in order: a localized content bundle (content/<locale>.json), a
// static content/index.html, else a plugin info card.
func renderPluginPage(w http.ResponseWriter, r *http.Request, d *common.Deps, entry data.PageEntryRow) {
	pluginDir := filepath.Join(pluginPagesDir, entry.PluginID, entry.PluginVersion)
	base := map[string]any{
		"title":     entry.Label,
		"theme":     d.CurrentState().Theme,
		"menuItems": d.Menu,
	}

	locale := httpx.ResolveLocale(w, r)
	if bundle, ok := loadContentBundle(filepath.Join(pluginDir, "content"), locale); ok {
		base["bundle"] = bundleView(bundle)
		httpx.Render("ui/pages/plugin_content.html", base)(w, r)
		return
	}

	if raw, err := os.ReadFile(filepath.Join(pluginDir, "content", "index.html")); err == nil {
		// The plugin bundle was Ed25519-verified at install; its page HTML is
		// trusted the same way its binary would be.
		base["pluginHTML"] = template.HTML(raw) //nolint:gosec
		httpx.Render("ui/pages/plugin_embed.html", base)(w, r)
		return
	}

	base["pluginHTML"] = template.HTML( //nolint:gosec
		"<p>" + template.HTMLEscapeString(entry.PluginName+" v"+entry.PluginVersion) + "</p>" +
			"<p class=\"empty\">This plugin registered a page but ships no page content.</p>")
	httpx.Render("ui/pages/plugin_embed.html", base)(w, r)
}

// loadContentBundle picks the best content/<locale>.json for the POS locale:
// exact match, prefix match (en -> en-US/en-GB), declared fallback, en-US,
// then any bundle.
func loadContentBundle(contentDir, locale string) (*contentBundle, bool) {
	files, err := os.ReadDir(contentDir)
	if err != nil {
		return nil, false
	}
	var names []string
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".json") {
			names = append(names, strings.TrimSuffix(f.Name(), ".json"))
		}
	}
	if len(names) == 0 {
		return nil, false
	}
	sort.Strings(names)

	pick := ""
	for _, n := range names {
		if strings.EqualFold(n, locale) {
			pick = n
			break
		}
	}
	if pick == "" {
		for _, n := range names {
			if strings.HasPrefix(strings.ToLower(n), strings.ToLower(locale)+"-") {
				pick = n
				break
			}
		}
	}
	if pick == "" {
		for _, fallback := range []string{"en-US", names[0]} {
			for _, n := range names {
				if n == fallback {
					pick = n
					break
				}
			}
			if pick != "" {
				break
			}
		}
	}

	raw, err := os.ReadFile(filepath.Join(contentDir, pick+".json"))
	if err != nil {
		return nil, false
	}
	var b contentBundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, false
	}
	// A bundle without entries isn't a content page.
	if len(b.Entries) == 0 {
		return nil, false
	}
	return &b, true
}

// bundleView groups bundle entries under their (sorted) categories for the
// template.
func bundleView(b *contentBundle) map[string]any {
	type entryView struct {
		Question, Answer string
		Sort             int
	}
	type categoryView struct {
		Name    string
		Sort    int
		Entries []entryView
	}

	catName := map[string]string{}
	catSort := map[string]int{}
	for _, c := range b.Categories {
		catName[c.ID] = c.Name
		catSort[c.ID] = c.SortOrder
	}

	grouped := map[string][]entryView{}
	for _, e := range b.Entries {
		grouped[e.Category] = append(grouped[e.Category], entryView{Question: e.Question, Answer: e.Answer, Sort: e.SortOrder})
	}

	cats := make([]categoryView, 0, len(grouped))
	for id, entries := range grouped {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Sort < entries[j].Sort })
		name := catName[id]
		if name == "" {
			name = id
		}
		cats = append(cats, categoryView{Name: name, Sort: catSort[id], Entries: entries})
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Sort < cats[j].Sort })

	return map[string]any{"RTL": b.RTL, "Categories": cats}
}
