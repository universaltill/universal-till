package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Overridable in tests.
var (
	builtinThemesDir = "web/public/themes"
	pluginThemesDir  = "./data/plugins"
)

// ThemeOption is one selectable UI theme: either a built-in CSS file under
// web/public/themes or a theme entry contributed by an installed plugin.
type ThemeOption struct {
	Key   string // value stored in the "theme" setting; also /themes/{key}.css
	Label string // rendered through T (ut-docs#2015): a plugin theme's Label
	// is a translator key, resolved via the plugin's own locales/ overlay
	// (ADR-0010; the entry-label contract itself is reference/
	// plugin-manifest.md's entries table, `label` row), same convention as a
	// page/export/report entry's label; a built-in's plain-text Label
	// (titleCase of the CSS filename) passes through T unchanged.
	//
	// EVERY consumer must resolve it, not just the Settings picker: the
	// cloud Design picker's copy goes through httpx.T in cloudThemeOptions
	// (ut-docs#2015 review) because it has no request locale of its own.
	Source string // "built-in" | plugin ID
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// themeConfig is the config_json contract for plugin_entries type='theme'.
type themeConfig struct {
	CSS string `json:"css"` // path of the stylesheet inside the plugin dir
}

// availableThemes lists built-in themes (disk override if present, else the
// binary's embedded defaults) followed by plugin themes.
func availableThemes(ctx context.Context, d *common.Deps) []ThemeOption {
	options := []ThemeOption{}

	if entries, err := fs.ReadDir(newPublicFallbackFS(builtinThemesDir, "public/themes"), "."); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".css") {
				continue
			}
			key := strings.TrimSuffix(name, ".css")
			options = append(options, ThemeOption{Key: key, Label: titleCase(key), Source: "built-in"})
		}
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Key < options[j].Key })

	if d.Db != nil {
		if rows, err := data.NewPluginRepo(d.Db).ListThemeEntries(ctx); err == nil {
			for _, row := range rows {
				label := row.Label
				if label == "" {
					label = row.EntryKey
				}
				options = append(options, ThemeOption{Key: row.EntryKey, Label: label, Source: row.PluginID})
			}
		}
	}
	return options
}

// cloudThemeOptions is availableThemes shaped for the cloud's Design picker
// (cloudsync_wire.go's DeviceExtra hook): {"key","label"} pairs where the key
// is the raw entry key the cloud sends back as a `set_setting theme`
// directive, and the label is the DISPLAY name — so it goes through the
// translator exactly as the Settings <select> does (ut-docs#2015 review;
// ThemeOption.Label is a translator key for a plugin theme). This hook runs
// on cloudsync's background goroutine with no request locale, so it resolves
// at the shop's configured default locale, the same choice print_api.go /
// kitchen_print.go / alerts.go make for their own non-request-bound surfaces.
func cloudThemeOptions(ctx context.Context, d *common.Deps) []map[string]string {
	locale := httpx.DefaultLocale()
	out := []map[string]string{}
	for _, opt := range availableThemes(ctx, d) {
		out = append(out, map[string]string{"key": opt.Key, "label": httpx.T(locale, opt.Label)})
	}
	return out
}

// resolvePluginThemeCSS maps a theme key to the stylesheet file of the plugin
// that registered it. Returns "" when no active plugin provides the key.
func resolvePluginThemeCSS(ctx context.Context, d *common.Deps, key string) string {
	if d.Db == nil {
		return ""
	}
	rows, err := data.NewPluginRepo(d.Db).ListThemeEntries(ctx)
	if err != nil {
		return ""
	}
	for _, row := range rows {
		if row.EntryKey != key {
			continue
		}
		var cfg themeConfig
		if err := json.Unmarshal([]byte(row.ConfigJSON), &cfg); err != nil || cfg.CSS == "" {
			return ""
		}
		path := filepath.Join(pluginThemesDir, row.PluginID, row.PluginVersion, cfg.CSS)
		// Keep the resolved path inside the plugin base dir (config is
		// plugin-supplied input).
		base := filepath.Clean(pluginThemesDir)
		if !strings.HasPrefix(filepath.Clean(path), base+string(os.PathSeparator)) {
			return ""
		}
		return path
	}
	return ""
}

// registerThemes serves theme stylesheets: /themes/{name}.css resolves to a
// built-in stylesheet or, failing that, to the CSS a plugin theme entry points
// at. Plugin themes can restyle the POS and reposition the screen panels via
// the pos-container grid areas.
func registerThemes(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /themes/{file}", func(w http.ResponseWriter, r *http.Request) {
		file := r.PathValue("file")
		key := strings.TrimSuffix(file, ".css")
		// Theme keys are single path segments; reject anything path-like.
		if key == "" || !strings.HasSuffix(file, ".css") ||
			strings.ContainsAny(key, `/\`) || strings.Contains(key, "..") {
			http.NotFound(w, r)
			return
		}

		if f, err := newPublicFallbackFS(builtinThemesDir, "public/themes").Open(key + ".css"); err == nil {
			defer f.Close()
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			_, _ = io.Copy(w, f)
			return
		}

		if path := resolvePluginThemeCSS(r.Context(), d, key); path != "" {
			if _, err := os.Stat(path); err == nil {
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
				http.ServeFile(w, r, path)
				return
			}
		}
		http.NotFound(w, r)
	})
}

// registerThemeSync wires GET /ui/theme-sync (ut-docs#2343). base.html polls
// it from every open page every 30s (mirrors GET /ui/pairing-notice's
// pattern), passing back the theme key it was rendered with. A theme applied
// via a cloud set_setting directive (ADR-0018) already lands in d.State the
// moment the directive is applied (SetSetting -> rederive -> LoadState,
// cloudsync_wire.go) -- exactly like a local Settings-page change -- so any
// FUTURE page render already shows it. The gap this closes is a kiosk
// session that stays on one already-rendered page for hours: the local
// Settings page forces a refresh with its own window.location.reload()
// (settings.html), but a directive landing in the background has no client
// to tell to reload. Answering with an out-of-band swap of the stylesheet
// <link> (id="theme-css") instead of a reload is the safer choice here: no
// navigation, so an in-progress sale's on-screen state is never disturbed --
// the offline-first "checkout must never be blocked" rule applies to a
// forced reload too, not just to the network being down.
func registerThemeSync(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/theme-sync", func(w http.ResponseWriter, r *http.Request) {
		live := d.CurrentState().Theme
		clientTheme := r.URL.Query().Get("theme")
		if live == "" || live == clientTheme {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if live == "default" {
			// "default" has no override CSS of its own (base.html renders
			// #theme-css with no href for it) -- swap to the same,
			// clearing whatever override was active, rather than pointing
			// at a /themes/default.css that was never served.
			fmt.Fprint(w, `<link id="theme-css" hx-swap-oob="true" rel="stylesheet">`)
			return
		}
		// url.PathEscape doubles as HTML-escaping here: every byte it would
		// otherwise leave unescaped (letters/digits/-._~) is inert in both
		// an href attribute and a URL path, so a theme key holding
		// HTML-breaking characters can't escape the attribute it's placed
		// in. Defence in depth -- nothing on the write path (cloud
		// directive or local settings) constrains the key's charset today.
		// No cache-busting query param needed here (unlike base.html's own
		// static <link>): the href's PATH changes with the theme key, so
		// the browser can never serve a stale cached response for the new
		// theme under the old one's URL.
		fmt.Fprintf(w, `<link id="theme-css" hx-swap-oob="true" rel="stylesheet" href="/themes/%s.css">`,
			url.PathEscape(live))
	})
}
