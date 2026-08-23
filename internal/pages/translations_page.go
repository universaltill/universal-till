package pages

import (
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerTranslations wires the manager translation editor (docs repo:
// architecture/translation-editor.md): every visible string — built-in POS
// keys and plugin-shipped keys — editable per locale in one place. Shop
// overrides win over base locale files and plugin overlays; "reset" deletes
// the override so the string falls back.
func registerTranslations(mux *http.ServeMux, d *common.Deps, i18n *config.I18n) {
	repo := data.NewTranslationRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	// requireManager gates on the "settings" action (039's catalog) via
	// canPerform — see country_settings_page.go's identical
	// requireManager for why (ut-docs#901/#902): the old raw
	// IsManager() check never saw canPerform's UT_AUTH=off escape hatch,
	// so this page 403'd permanently under the dev/CI auth-bypass. No
	// change to gated (UT_AUTH on) behavior.
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// reload pushes the persisted overrides into the live translator so
	// edits apply immediately, no restart.
	reload := func(r *http.Request) {
		if overrides, err := repo.ListOverrides(r.Context()); err == nil {
			i18n.SetShopOverrides(overrides)
		}
	}

	type rowVM struct {
		Key, Value, Source, Reference string
	}
	renderTable := func(w http.ResponseWriter, r *http.Request, editLocale, q string) {
		entries := i18n.Entries(editLocale)
		q = strings.ToLower(strings.TrimSpace(q))
		rows := make([]rowVM, 0, len(entries))
		for _, e := range entries {
			if q != "" && !strings.Contains(strings.ToLower(e.Key), q) &&
				!strings.Contains(strings.ToLower(e.Value), q) &&
				!strings.Contains(strings.ToLower(e.Reference), q) {
				continue
			}
			rows = append(rows, rowVM(e))
		}
		httpx.RenderPartial("ui/partials/translations_table.html", map[string]any{
			"editLocale": editLocale,
			"q":          q,
			"rows":       rows,
		})(w, r)
	}

	mux.HandleFunc("GET /translations", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		editLocale := strings.TrimSpace(r.URL.Query().Get("edit_locale"))
		if editLocale == "" {
			editLocale = httpx.ResolveLocale(w, r)
		}
		httpx.Render("ui/pages/translations.html", map[string]any{
			"title":      "Translations",
			"theme":      d.CurrentState().Theme,
			"menuItems":  d.MenuSnapshot(),
			"editLocale": editLocale,
			"locales":    i18n.Available(),
		})(w, r)
	})

	mux.HandleFunc("GET /ui/translations-table", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireManager(w, r); !ok {
			return
		}
		renderTable(w, r, strings.TrimSpace(r.URL.Query().Get("edit_locale")), r.URL.Query().Get("q"))
	})

	mux.HandleFunc("POST /api/translations/set", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		_ = r.ParseForm()
		editLocale := strings.TrimSpace(r.Form.Get("edit_locale"))
		key := strings.TrimSpace(r.Form.Get("key"))
		value := r.Form.Get("value")
		if editLocale == "" || key == "" || strings.TrimSpace(value) == "" {
			http.Error(w, "edit_locale, key and value required", http.StatusBadRequest)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		if err := repo.SetOverride(r.Context(), editLocale, key, value, actor.ID, now); err != nil {
			http.Error(w, "could not save override", http.StatusInternalServerError)
			return
		}
		reload(r)
		_ = posRepo.InsertAudit(r.Context(), nil, actor.ID, "translation", editLocale+"/"+key,
			"translation_override_set", map[string]string{"locale": editLocale, "key": key}, now, "")
		renderTable(w, r, editLocale, r.Form.Get("q"))
	})

	mux.HandleFunc("POST /api/translations/clear", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		_ = r.ParseForm()
		editLocale := strings.TrimSpace(r.Form.Get("edit_locale"))
		key := strings.TrimSpace(r.Form.Get("key"))
		if editLocale == "" || key == "" {
			http.Error(w, "edit_locale and key required", http.StatusBadRequest)
			return
		}
		if _, err := repo.ClearOverride(r.Context(), editLocale, key); err != nil {
			http.Error(w, "could not clear override", http.StatusInternalServerError)
			return
		}
		reload(r)
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actor.ID, "translation", editLocale+"/"+key,
			"translation_override_cleared", map[string]string{"locale": editLocale, "key": key}, now, "")
		renderTable(w, r, editLocale, r.Form.Get("q"))
	})
}
