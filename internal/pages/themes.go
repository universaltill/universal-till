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
	Key    string // value stored in the "theme" setting; also /themes/{key}.css
	Label  string
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
