package pages

import (
	"io/fs"
	"net/http"

	"github.com/universaltill/universal-till/internal/help"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/web"
)

// registerHelp serves the user manual (ut-docs#325): a two-pane shell with a
// topic tree + search on the inline-start side and the selected topic —
// rendered from Markdown embedded under web/help/<lang>/ — on the other.
// The index is built once here and closed over; topics are embedded, so
// everything below works fully offline and needs no request-scoped state.
func registerHelp(mux *http.ServeMux, d *common.Deps) {
	sub, err := fs.Sub(web.FS, "help")
	if err != nil {
		panic("help: embedded topic tree missing: " + err.Error())
	}
	idx, err := help.LoadTopics(sub)
	if err != nil {
		// A malformed topic file is a build defect — fail at startup, loudly,
		// rather than serving a silently incomplete manual.
		panic(err.Error())
	}

	// helpPageFiles is the full-page template set. help.html and its two
	// partials are parsed per request via RenderWith (not the global Render
	// partial list) so the manual's templates stay local to this page.
	helpPageFiles := []string{
		"ui/layouts/base.html",
		"ui/pages/help.html",
		"ui/partials/nav.html",
		"ui/partials/help_topics.html",
		"ui/partials/help_topic.html",
	}

	renderFull := func(w http.ResponseWriter, r *http.Request, topicID string) {
		locale := httpx.ResolveLocale(w, r)
		sections := idx.Sections(locale)
		var (
			topic      *help.Topic
			translated = true
		)
		if topicID == "" {
			// Default landing topic: the first topic of the first section
			// (quick-start sorts there by front-matter order).
			if len(sections) > 0 && len(sections[0].Topics) > 0 {
				first := sections[0].Topics[0]
				topic, translated = idx.Get(locale, first.ID)
			}
		} else {
			topic, translated = idx.Get(locale, topicID)
			if topic == nil {
				http.NotFound(w, r)
				return
			}
		}
		data := map[string]any{
			"title":      "Help",
			"theme":      d.CurrentState().Theme,
			"menuItems":  d.Menu,
			"Sections":   sections,
			"Topic":      topic,
			"Translated": translated,
			"CurrentID":  currentTopicID(topic),
		}
		httpx.RenderWith(helpPageFiles, httpx.FuncsFor(locale))("base", data)(w, r)
	}

	mux.HandleFunc("GET /help", func(w http.ResponseWriter, r *http.Request) {
		renderFull(w, r, "")
	})

	mux.HandleFunc("GET /help/{topic}", func(w http.ResponseWriter, r *http.Request) {
		topicID := r.PathValue("topic")
		if r.Header.Get("HX-Request") == "true" {
			locale := httpx.ResolveLocale(w, r)
			topic, translated := idx.Get(locale, topicID)
			if topic == nil {
				http.NotFound(w, r)
				return
			}
			httpx.RenderPartial("ui/partials/help_topic.html", map[string]any{
				"Topic":      topic,
				"Translated": translated,
			})(w, r)
			return
		}
		// A fresh (non-htmx) GET renders the whole shell with this topic
		// pre-selected, so /help/<id> is directly linkable and back/forward
		// works with hx-push-url.
		renderFull(w, r, topicID)
	})

	mux.HandleFunc("GET /help/search", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		q := r.URL.Query().Get("q")
		sections := idx.Sections(locale)
		empty := false
		if matches := idx.Search(locale, q); len(matches) > 0 {
			sections = groupMatches(matches)
		} else if q != "" {
			sections = nil
			empty = true
		}
		// q == "" restores the full tree (the cashier cleared the box).
		httpx.RenderPartial("ui/partials/help_topics.html", map[string]any{
			"Sections":  sections,
			"CurrentID": "",
			"NoResults": empty,
		})(w, r)
	})
}

// currentTopicID guards the nil default-landing case (empty manual).
func currentTopicID(t *help.Topic) string {
	if t == nil {
		return ""
	}
	return t.ID
}

// groupMatches folds ranked search results back into the Section shape the
// topic-list partial renders, grouping by section in order of each section's
// best-ranked hit so rank stays visible.
func groupMatches(matches []*help.Topic) []help.Section {
	var out []help.Section
	pos := map[string]int{}
	for _, t := range matches {
		i, ok := pos[t.Section]
		if !ok {
			i = len(out)
			pos[t.Section] = i
			out = append(out, help.Section{Name: t.Section})
		}
		out[i].Topics = append(out[i].Topics, t)
	}
	return out
}
