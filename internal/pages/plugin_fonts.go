package pages

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Overridable in tests, same convention as pluginIconsDir; pages.Init
// repoints it at paths.Plugins().
var pluginFontsDir = "./data/plugins"

// pluginFontCSP is sent on every font response: a font fetches nothing and
// runs nothing, and the sandbox keeps it inert if opened top-level.
const pluginFontCSP = "default-src 'none'; sandbox"

// registerPluginFonts serves the font subsets language bundles ship in
// fonts/ (ADR-0160 §8, ut-docs#4374) from the till's own disk — never a CDN
// (ADR-0003) — plus one generated stylesheet that declares them:
//
//   - GET /plugin-fonts/{plugin}/{version}/{file}: one woff2 file, served
//     only when the plugin manager lists exactly that (plugin, version,
//     file) — an active language plugin's fonts/*.woff2. Nothing else a
//     bundle ships is reachable here.
//   - GET /plugin-fonts/bundle-fonts.css: an @font-face per listed font and
//     --ut-bundle-fonts, which app.css's body font stack consumes AFTER the
//     generic sans-serif, so a bundle font only draws glyphs the system
//     fonts lack.
//
// Both are auth-exempt (internal/auth/middleware.go): login, setup and the
// self-order kiosk render a bundle's script too, and fonts are not secret.
func registerPluginFonts(mux *http.ServeMux, d *common.Deps) {
	registerPluginFontRoutes(mux, d.Pm.BundleFonts)
}

func registerPluginFontRoutes(mux *http.ServeMux, fonts func() []plugins.BundleFont) {
	mux.HandleFunc("GET /plugin-fonts/bundle-fonts.css", func(w http.ResponseWriter, r *http.Request) {
		css := bundleFontsCSS(fonts())
		sum := sha256.Sum256(css)
		h := w.Header()
		h.Set("Content-Type", "text/css; charset=utf-8")
		h.Set("X-Content-Type-Options", "nosniff")
		// The set changes on install/enable/disable while a page's
		// persistent shell keeps the <link>; revalidate on every load.
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
		http.ServeContent(w, r, "bundle-fonts.css", time.Time{}, bytes.NewReader(css))
	})

	mux.HandleFunc("GET /plugin-fonts/{plugin}/{version}/{file}", func(w http.ResponseWriter, r *http.Request) {
		want := plugins.BundleFont{PluginID: r.PathValue("plugin"), Version: r.PathValue("version"), File: r.PathValue("file")}
		listed := false
		for _, f := range fonts() {
			if f == want {
				listed = true
				break
			}
		}
		// The listing already validated id, version and file name
		// (plugins.syncBundleFonts); re-check before touching the disk.
		if !listed || plugins.ValidatePluginID(want.PluginID) != nil ||
			plugins.ValidatePluginVersion(want.Version) != nil || !plugins.IsBundleFontName(want.File) {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(filepath.Join(pluginFontsDir, want.PluginID, want.Version, "fonts", want.File))
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
		h.Set("Content-Type", "font/woff2")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", pluginFontCSP)
		// @font-face fetches are CORS-mode: a plugin page's empty-sandbox
		// srcdoc frame (opaque origin, plugin_page.go pluginFrameTmpl) is
		// refused the font without this. The file is public and the route
		// auth-exempt already, so any origin may read it.
		h.Set("Access-Control-Allow-Origin", "*")
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	})
}

// bundleFontsCSS renders the stylesheet for fonts. Family names are
// positional ("ut-bundle-N"), and every URL part has passed
// ValidatePluginID / ValidatePluginVersion / IsBundleFontName, none of which
// admits a quote, backslash or parenthesis — so nothing here needs escaping.
// An empty set declares nothing, leaving app.css's var() fallback in force.
func bundleFontsCSS(fonts []plugins.BundleFont) []byte {
	var b bytes.Buffer
	b.WriteString("/* Language-bundle fonts (ADR-0160 §8). Generated per request. */\n")
	if len(fonts) == 0 {
		return b.Bytes()
	}
	stack := ""
	for i, f := range fonts {
		family := fmt.Sprintf("ut-bundle-%d", i+1)
		fmt.Fprintf(&b, `@font-face{font-family:"%s";src:url("/plugin-fonts/%s/%s/%s") format("woff2");font-display:swap}`+"\n",
			family, f.PluginID, f.Version, f.File)
		if i > 0 {
			stack += ","
		}
		stack += `"` + family + `"`
	}
	fmt.Fprintf(&b, ":root{--ut-bundle-fonts:%s}\n", stack)
	return b.Bytes()
}
