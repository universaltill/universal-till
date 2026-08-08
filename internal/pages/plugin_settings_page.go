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
	"github.com/universaltill/universal-till/internal/plugins"
)

// isSecretSettingKey reports whether a plugin setting holds a credential that
// should be masked (rendered as a password field, value never sent to the
// page). Heuristic on the key name — covers api keys, tokens, secrets,
// passwords, and the connector auth value.
func isSecretSettingKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range []string{"secret", "token", "password", "passwd", "api_key", "apikey", "auth_value", "private_key"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return strings.HasSuffix(k, "_key") || k == "key"
}

// Generic plugin settings editor (docs: architecture/ai-plugin.md): any
// installed plugin whose manifest declares settings gets an edit form.
// This is the surface the AI plugin's endpoint/model settings use, and
// what payment-terminal plugins will use for sandbox toggles etc.
func registerPluginSettings(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewPluginRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	type settingView struct {
		Key, Value string
		Secret     bool // rendered masked; its value is never sent to the page
		IsSet      bool // a value already exists (for the "leave blank to keep" hint)
		PerTill    bool // register-scoped: this till's own value, never synced
	}

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
			sv := settingView{Key: row.Key, Value: v, Secret: isSecretSettingKey(row.Key), PerTill: row.Scope == "register"}
			if sv.Secret {
				sv.IsSet = v != ""
				sv.Value = "" // never render a secret's value into the page
			}
			views = append(views, sv)
		}
		httpx.Render("ui/pages/plugin_settings.html", map[string]any{
			"title":     "Plugin settings",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
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
			val := strings.TrimSpace(form[0])
			// Secret fields aren't pre-filled, so a blank submission means
			// "keep the current value" rather than "clear it".
			if val == "" && isSecretSettingKey(row.Key) {
				continue
			}
			raw, err := json.Marshal(val)
			if err != nil {
				continue
			}
			if string(raw) == row.ValueJSON {
				continue
			}
			// Write back into the row's own scope: a register-scoped setting
			// (per-till, e.g. a card reader id) must not become shop-wide.
			if err := repo.UpsertPluginSettingScoped(r.Context(), pluginID, row.Key, string(raw), row.Scope); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			changed++
		}
		if changed > 0 {
			// A setting can feed a plugin's ".ask" answers (settings_get host
			// fn) — drop cached answers or the till keeps charging the old
			// rate until an unrelated reload (ut-docs#222 review finding).
			plugins.SharedBus(d.Db).BumpGeneration()
		}
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "plugin", pluginID, "plugin_settings_saved",
			map[string]any{"changed": changed}, time.Now().UTC().Format(time.RFC3339), "")
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span>✓ %s (%d)</span>`, httpx.T(locale, "plugins.settings.saved"), changed)
	})
}
