package pages

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Overridable in tests, same convention as pluginThemesDir/pluginPagesDir.
// The "./data/plugins" literal is only the pre-Init fallback; pages.Init
// repoints this at paths.Plugins() (the resolved, per-platform data dir)
// once paths.Init has run.
var pluginIconsDir = "./data/plugins"

// registerPluginIcons serves icon files an installed plugin ships alongside
// its bundle (manifest.json "icon_path" on an entry) — the icon shown on the
// button that entry contributes. Route: GET /plugin-icons/{plugin}/{version}/{file...}.
//
// icon_path is plugin-supplied input (it lands in plugin_entries.icon_path
// unsanitized at install time — internal/plugins/manifest.go PersistManifest);
// the resolved path is Clean()'d and checked against the plugin's own base
// dir before serving, the same guard resolvePluginThemeCSS uses for theme CSS.
func registerPluginIcons(mux *http.ServeMux) {
	mux.HandleFunc("GET /plugin-icons/{plugin}/{version}/{file...}", func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.PathValue("plugin")
		version := r.PathValue("version")
		file := r.PathValue("file")
		if pluginID == "" || version == "" || file == "" ||
			strings.Contains(pluginID, "..") || strings.Contains(version, "..") {
			http.NotFound(w, r)
			return
		}

		base := filepath.Clean(filepath.Join(pluginIconsDir, pluginID, version))
		path := filepath.Clean(filepath.Join(base, file))
		if path != base && !strings.HasPrefix(path, base+string(os.PathSeparator)) {
			http.NotFound(w, r)
			return
		}

		if info, err := os.Stat(path); err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	})
}
