package pages

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Generic plugin settings editor (docs: architecture/ai-plugin.md): any
// installed plugin whose manifest declares settings gets an edit form.
// This is the surface the AI plugin's endpoint/model settings use, and
// what payment-terminal plugins will use for sandbox toggles etc.
func registerPluginSettings(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewPluginRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	type settingView struct{ Key, Value string }

	mux.HandleFunc("GET /plugins/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Redirect(w, r, "/plugins", http.StatusSeeOther)
			return
		}
		pluginID := r.PathValue("id")
		rows, err := repo.ListPluginSettings(r.Context(), pluginID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var views []settingView
		for _, row := range rows {
			var v string
			if json.Unmarshal([]byte(row.ValueJSON), &v) != nil {
				v = row.ValueJSON // non-string JSON edits raw
			}
			views = append(views, settingView{Key: row.Key, Value: v})
		}
		httpx.Render("ui/pages/plugin_settings.html", map[string]any{
			"title":     "Plugin settings",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.Menu,
			"PluginID":  pluginID,
			"Settings":  views,
		})(w, r)
	})

	mux.HandleFunc("POST /api/plugins/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		pluginID := r.PathValue("id")
		_ = r.ParseForm()
		rows, err := repo.ListPluginSettings(r.Context(), pluginID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		changed := 0
		for _, row := range rows {
			// Only keys the plugin declared are writable — the form cannot
			// invent settings.
			form, ok := r.Form["setting_"+row.Key]
			if !ok || len(form) == 0 {
				continue
			}
			raw, err := json.Marshal(strings.TrimSpace(form[0]))
			if err != nil {
				continue
			}
			if string(raw) == row.ValueJSON {
				continue
			}
			if err := repo.UpsertPluginSetting(r.Context(), pluginID, row.Key, string(raw)); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			changed++
		}
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "plugin", pluginID, "plugin_settings_saved",
			map[string]any{"changed": changed}, time.Now().UTC().Format(time.RFC3339), "")
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span>✓ %s (%d)</span>`, httpx.T(locale, "plugins.settings.saved"), changed)
	})
}
