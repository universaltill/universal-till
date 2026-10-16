package pages

import (
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/i18nmeta"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	locales "github.com/universaltill/universal-till/web/locales"
)

// Translate mode (ADR-0160 §7, ut-docs#4370). A manager turns it on from
// the translation editor; every page then renders its strings tagged with
// their keys (httpx.withTranslateMode), and Alt+click or a long press on a
// string opens the key panel: the key's metadata, an Open in Weblate link
// and QR code, a shop override, and the page's areas. The till only builds
// the link — no Weblate token, no call to Weblate.
//
// Access: the override editor's own permission ("settings", see
// registerTranslations' requireManager), checked on every request — the
// cookie is only the manager's preference, so a cashier who signs in on the
// same browser afterwards gets plain pages. Never on a self-order kiosk
// (display.mode=self_order) and never on the customer pages themselves:
// the toggle, the panel and the override endpoint all refuse there, and
// the T helper stops tagging.

// translateModeCookie holds the preference ("1" = on).
const translateModeCookie = "ut_translate_mode"

// selfOrderPath reports whether path is one of the self-order kiosk's own
// customer pages or APIs.
func selfOrderPath(path string) bool {
	return path == "/self-order" || strings.HasPrefix(path, "/self-order/") ||
		strings.HasPrefix(path, "/api/self-order")
}

// translateModeAllowed: the override editor's permission, on a till that is
// not a self-order kiosk, on a page that is not a customer page.
func translateModeAllowed(d *common.Deps, r *http.Request) bool {
	if selfOrderPath(r.URL.Path) {
		return false
	}
	if !translateModeOffered(d, r) {
		return false
	}
	return canPerform(d, r, "settings")
}

// translateModeOffered: this till offers translate mode at all — not a
// self-order kiosk, and not the public demo (ADR-0113: no Weblate links or
// shop overrides from a demo visitor; its routes are demo-denied too).
func translateModeOffered(d *common.Deps, r *http.Request) bool {
	if d.Cfg != nil && d.Cfg.Demo {
		return false
	}
	return !inSelfOrderMode(r.Context(), d)
}

// translateModeActive is the per-request check httpx.InitTranslateMode is
// wired to: the preference cookie, then translateModeAllowed. The cookie is
// read first so a normal request costs nothing extra.
func translateModeActive(d *common.Deps, r *http.Request) bool {
	c, err := r.Cookie(translateModeCookie)
	if err != nil || c.Value != "1" {
		return false
	}
	return translateModeAllowed(d, r)
}

var (
	coreKeysOnce sync.Once
	coreKeys     map[string]bool
)

// isCoreKey reports whether key is one of the till's own en.json keys —
// the ones in the universal-till Weblate project. A plugin's strings are
// translated in that plugin, so they get no Weblate link here.
func isCoreKey(key string) bool {
	coreKeysOnce.Do(func() {
		coreKeys = map[string]bool{}
		raw, err := locales.FS.ReadFile("en.json")
		if err != nil {
			logging.L().Warnf("translate mode: read en.json: %v", err)
			return
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			logging.L().Warnf("translate mode: parse en.json: %v", err)
			return
		}
		for k := range m {
			coreKeys[k] = true
		}
	})
	return coreKeys[key]
}

// splitList parses a comma- or space-separated query value into distinct,
// non-empty items, capped so a crafted URL can't make the panel huge.
func splitList(v string, limit int) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		if seen[f] || len(f) > 200 {
			continue
		}
		seen[f] = true
		out = append(out, f)
		if len(out) == limit {
			break
		}
	}
	return out
}

// servedLocale is the locale whose strings this request actually gets: the
// request's locale when the till has it, else its base language ("en-US"
// -> "en"). The panel shows, overrides and links to that one, so an
// override lands where T reads it and the Weblate link names a language
// the project has.
func servedLocale(i18n *config.I18n, locale string) string {
	for _, a := range i18n.Available() {
		if strings.EqualFold(a, locale) {
			return a
		}
	}
	base, _, _ := strings.Cut(strings.ReplaceAll(locale, "_", "-"), "-")
	return strings.ToLower(base)
}

func registerTranslateMode(mux *http.ServeMux, d *common.Deps, i18n *config.I18n, repo *data.TranslationRepo, posRepo *data.POSRepo, reload func(*http.Request)) {
	refuse := func(w http.ResponseWriter, r *http.Request) {
		if selfOrderPath(r.URL.Path) || inSelfOrderMode(r.Context(), d) {
			common.LocalizedError(w, r, http.StatusForbidden, "translations.mode.refused_kiosk")
			return
		}
		common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
	}

	// POST /api/translate-mode on=1|0. Turning it off is always allowed (it
	// only clears the preference); turning it on needs translateModeAllowed.
	mux.HandleFunc("POST /api/translate-mode", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		on := r.Form.Get("on") == "1"
		if on && !translateModeAllowed(d, r) {
			refuse(w, r)
			return
		}
		c := &http.Cookie{Name: translateModeCookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil}
		action := "translate_mode_off"
		if on {
			c.Value = "1"
			action = "translate_mode_on"
		} else {
			c.MaxAge = -1
		}
		http.SetCookie(w, c)
		if u, ok := auth.FromContext(r.Context()); ok {
			_ = posRepo.InsertAudit(r.Context(), nil, u.ID, "translation", "translate_mode", action, nil,
				time.Now().UTC().Format(time.RFC3339), "")
		}
		// A full reload: every string on the page re-renders with (or
		// without) its key, and the shell signature changes with it.
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
	})

	renderPanel := func(w http.ResponseWriter, r *http.Request, key string, keys, nss []string, status string) {
		locale := servedLocale(i18n, httpx.RequestLocale(r))
		var entry *config.TranslationEntry
		for _, e := range i18n.Entries(locale) {
			if e.Key == key {
				e := e
				entry = &e
				break
			}
		}
		if entry == nil {
			common.LocalizedError(w, r, http.StatusNotFound, "translations.mode.unknown_key")
			return
		}
		cat, err := i18nmeta.Builtin()
		if err != nil {
			logging.L().Warnf("translate mode: key metadata: %v", err)
		}
		vm := map[string]any{
			"key":       key,
			"keys":      keys,
			"nsParam":   strings.Join(nss, ","),
			"keysParam": strings.Join(keys, ","),
			"locale":    locale,
			"value":     i18n.T(locale, key),
			"reference": entry.Reference,
			"override":  entry.Source == "shop",
			"status":    status,
		}
		if entry.Source == "shop" {
			vm["overrideValue"] = entry.Value
		}
		if cat != nil {
			area := cat.AreaFor(key)
			vm["area"] = area
			if m, ok := cat.Lookup(key); ok {
				vm["meta"] = m
			}
			areaSet := map[string]bool{area: true}
			for _, ns := range nss {
				areaSet[cat.AreaFor(ns+".x")] = true
			}
			pageAreas := make([]string, 0, len(areaSet))
			for a := range areaSet {
				pageAreas = append(pageAreas, a)
			}
			sort.Strings(pageAreas)
			vm["pageAreas"] = pageAreas
			if isCoreKey(key) {
				link := i18nmeta.WeblateURL(area, locale, key)
				vm["weblateURL"] = link
				if png, err := qrcode.Encode(link, qrcode.Medium, 200); err == nil {
					// A fixed data:image/png prefix over our own encoder's bytes.
					vm["qr"] = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
				}
			}
		}
		httpx.RenderPartial("ui/partials/translate_panel.html", vm)(w, r)
	}

	// GET /ui/translate-mode/panel?key=K&keys=K,K2&ns=pos,common — the key
	// panel's body. keys: every key on the pressed element (a chooser when
	// it carries more than one); ns: the namespaces on the page, for its
	// list of areas.
	mux.HandleFunc("GET /ui/translate-mode/panel", func(w http.ResponseWriter, r *http.Request) {
		if !translateModeActive(d, r) {
			refuse(w, r)
			return
		}
		q := r.URL.Query()
		key := strings.TrimSpace(q.Get("key"))
		keys := splitList(q.Get("keys"), 20)
		if key == "" && len(keys) > 0 {
			key = keys[0]
		}
		renderPanel(w, r, key, keys, splitList(q.Get("ns"), 200), "")
	})

	// POST /api/translate-mode/override key, value, op=save|reset: the
	// existing shop override layer (same repo, reload and audit actions as
	// /api/translations/set|clear), for this request's language.
	mux.HandleFunc("POST /api/translate-mode/override", func(w http.ResponseWriter, r *http.Request) {
		if !translateModeActive(d, r) {
			refuse(w, r)
			return
		}
		_ = r.ParseForm()
		locale := servedLocale(i18n, httpx.RequestLocale(r))
		key := strings.TrimSpace(r.Form.Get("key"))
		value := r.Form.Get("value")
		if key == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		actor, _ := auth.FromContext(r.Context())
		now := time.Now().UTC().Format(time.RFC3339)
		status := "saved"
		switch r.Form.Get("op") {
		case "reset":
			if _, err := repo.ClearOverride(r.Context(), locale, key); err != nil {
				http.Error(w, "could not clear override", http.StatusInternalServerError)
				return
			}
			status = "reset"
			reload(r)
			_ = posRepo.InsertAudit(r.Context(), nil, actor.ID, "translation", locale+"/"+key,
				"translation_override_cleared", map[string]string{"locale": locale, "key": key, "via": "translate_mode"}, now, "")
		default:
			if strings.TrimSpace(value) == "" {
				status = "empty"
				break
			}
			if err := repo.SetOverride(r.Context(), locale, key, value, actor.ID, now); err != nil {
				http.Error(w, "could not save override", http.StatusInternalServerError)
				return
			}
			reload(r)
			_ = posRepo.InsertAudit(r.Context(), nil, actor.ID, "translation", locale+"/"+key,
				"translation_override_set", map[string]string{"locale": locale, "key": key, "via": "translate_mode"}, now, "")
		}
		renderPanel(w, r, key, splitList(r.Form.Get("keys"), 20), splitList(r.Form.Get("ns"), 200), status)
	})
}
