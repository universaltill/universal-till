package pages

import (
	"log"
	"net/http"
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
