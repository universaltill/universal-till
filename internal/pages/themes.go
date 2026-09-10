package pages

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
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
