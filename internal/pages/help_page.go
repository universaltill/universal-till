package pages

import (
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/manual"
	"github.com/universaltill/universal-till/internal/pages/common"
	uiassets "github.com/universaltill/universal-till/web"
)

// The manual is read-only content baked into the binary, so it is parsed once
// on first use rather than per request — rendering ~16 Markdown topics × 4
// locales on every page view would be pointless work on a Pi.
var (
	libOnce sync.Once
	lib     *manual.Library
	libErr  error
)

// Library returns the loaded user manual. Other pages use it for the "?"
// links (route → topic).
func Library() (*manual.Library, error) {
	libOnce.Do(func() {
		lib, libErr = manual.Load(uiassets.HelpFS, "help")
		if libErr != nil {
			log.Printf("help: loading manual: %v", libErr)
		}
	})
	return lib, libErr
}

func registerHelp(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/help", func(w http.ResponseWriter, r *http.Request) {
		renderHelpPage(w, r, d, "")
	})
	mux.HandleFunc("GET /help/search", func(w http.ResponseWriter, r *http.Request) {
		l, err := Library()
		if err != nil {
			http.Error(w, "manual unavailable", http.StatusInternalServerError)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		q := r.URL.Query().Get("q")
		httpx.RenderPartial("ui/partials/help_results.html", map[string]any{
			"results": l.Search(locale, q),
			"query":   q,
		})(w, r)
	})
	mux.HandleFunc("GET /help/{topic}", func(w http.ResponseWriter, r *http.Request) {
		renderHelpPage(w, r, d, r.PathValue("topic"))
	})
	// Generated topic screenshots (make docs-shots → web/help/img/…),
	// embedded alongside the topics so the manual's images work with the
	// line down, same as its prose.
	mux.HandleFunc("GET /help/img/{locale}/{file}", func(w http.ResponseWriter, r *http.Request) {
		locale, file := r.PathValue("locale"), r.PathValue("file")
		// Path values are single segments, but encoded dots/slashes decode
		// INTO them — reject anything that isn't a bare "<slug>.png" lookup,
		// same rule as manual.Topic's id check. Only PNGs are ever generated,
		// so only PNGs are served.
		if strings.ContainsAny(locale, "/\\") || strings.Contains(locale, "..") ||
			strings.ContainsAny(file, "/\\") || strings.Contains(file, "..") ||
			!strings.HasSuffix(file, ".png") {
			http.NotFound(w, r)
			return
		}
		b, err := fs.ReadFile(uiassets.HelpFS, path.Join("help", "img", locale, file))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(b)
	})
}

// renderHelpPage serves the manual. A plain request renders the whole
// two-pane page so a topic URL is shareable and works on a cold load; an htmx
// request renders just the reading panel so navigating the tree doesn't
// re-render the shell.
func renderHelpPage(w http.ResponseWriter, r *http.Request, d *common.Deps, topicID string) {
	l, err := Library()
	if err != nil {
		http.Error(w, "manual unavailable", http.StatusInternalServerError)
		return
	}
	locale := httpx.ResolveLocale(w, r)

	var topic *manual.Topic
	if topicID != "" {
		t, ok := l.Topic(locale, topicID)
		if !ok {
			// An unknown topic is a 404, not a silent redirect to the index —
			// a stale "?" link should be visible, not swallowed.
			http.NotFound(w, r)
			return
		}
		topic = t
	}

	data := map[string]any{
		"title":     "Help",
		"theme":     d.CurrentState().Theme,
		"menuItems": d.Menu,
		"sections":  l.Tree(locale),
		"topic":     topic,
	}
	if topic != nil {
		data["title"] = topic.Title
		data["currentID"] = topic.ID
	}

	if strings.EqualFold(r.Header.Get("HX-Request"), "true") {
		httpx.RenderPartial("ui/partials/help_topic.html", data)(w, r)
		return
	}
	httpx.Render("ui/pages/help.html", data)(w, r)
}
