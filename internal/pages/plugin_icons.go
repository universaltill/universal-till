package pages

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/plugins"
)

// Overridable in tests, same convention as pluginThemesDir/pluginPagesDir.
// The "./data/plugins" literal is only the pre-Init fallback; pages.Init
// repoints this at paths.Plugins() (the resolved, per-platform data dir)
// once paths.Init has run.
var pluginIconsDir = "./data/plugins"

// pluginIconTypes is the whole set of files the icon route will serve,
// keyed by lower-cased extension, with the Content-Type sent for each
// (never sniffed, never taken from the OS mime table). Real plugins ship
// PNG icons (ut-plugin-faq, layout-salon: assets/icon.png); the rest are
// the image formats a plugin author could reasonably use. Anything else a
// bundle ships -- docs HTML, JS, JSON -- is not reachable here
// (ut-docs#2892 security review).
var pluginIconTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
	".ico":  "image/x-icon",
	".svg":  "image/svg+xml",
}

// pluginIconCSP is sent on every icon response: no fetches, no script, and
// a sandbox (opaque origin, scripts off) so an SVG that carries <script>
// cannot run it even when opened top-level on the till origin. Inline
// styles stay allowed because SVG icons commonly use style attributes.
const pluginIconCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

// registerPluginIcons serves icon files an installed plugin ships alongside
// its bundle (manifest.json "icon_path" on an entry) -- the icon shown on the
// button that entry contributes. Route: GET /plugin-icons/{plugin}/{version}/{file...}.
// The route is auth-exempt (internal/auth/middleware.go), so it serves
// images only, with an explicit type, nosniff and a sandboxing CSP.
//
// icon_path is plugin-supplied input (it lands in plugin_entries.icon_path
// unsanitized at install time -- internal/plugins/manifest.go PersistManifest);
// the resolved path is Clean()'d and checked against the plugin's own base
// dir before serving, the same guard resolvePluginThemeCSS uses for theme CSS.
func registerPluginIcons(mux *http.ServeMux) {
	mux.HandleFunc("GET /plugin-icons/{plugin}/{version}/{file...}", func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.PathValue("plugin")
		version := r.PathValue("version")
		file := r.PathValue("file")
		if file == "" || plugins.ValidatePluginID(pluginID) != nil || plugins.ValidatePluginVersion(version) != nil {
			http.NotFound(w, r)
			return
		}
		contentType, ok := pluginIconTypes[strings.ToLower(filepath.Ext(file))]
		if !ok {
			http.NotFound(w, r)
			return
		}

		base := filepath.Clean(filepath.Join(pluginIconsDir, pluginID, version))
		path := filepath.Clean(filepath.Join(base, file))
		if !strings.HasPrefix(path, base+string(os.PathSeparator)) {
			http.NotFound(w, r)
			return
		}

		f, err := os.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", contentType)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", pluginIconCSP)
		// ServeContent (not ServeFile): no directory listing, no
		// index.html redirect, and it keeps the Content-Type set above.
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	})
}
