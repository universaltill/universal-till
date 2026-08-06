// Package manual is the till's built-in user manual — the topic tree, the
// search, and the route→topic registry the "?" links resolve through.
//
// Topics are Markdown files under web/help/<locale>/<id>.md with a small
// front-matter header, embedded into the binary. That shape is deliberate:
//
//   - Markdown files, not locale keys, because the manual is prose with
//     screenshots and headings, and the previous help page's flat
//     help.feat.<id>.s1/.s2/.s3 locale keys couldn't carry any of that.
//   - Embedded, because the manual has to work with the network off — a shop
//     whose line is down is exactly when someone reads it.
//   - The tree is derived from the files, so adding a topic is adding a file;
//     no Go change, and nothing to forget to register.
package manual

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// FallbackLocale is served whenever a topic hasn't been translated yet. A
// missing translation must never produce a blank page.
const FallbackLocale = "en"

// Topic is one page of the manual in one locale.
type Topic struct {
	ID       string
	Title    string
	Section  string
	Summary  string
	Order    int
	Routes   []string
	Keywords []string

	// Locale the content actually came from — may differ from the requested
	// locale when falling back.
	Locale string
	// Translated is false when this is English served in place of a missing
	// translation, so the page can say so rather than pretending.
	Translated bool

	Markdown string
	HTML     template.HTML

	plain      string // body as plain text, for snippets
	plainLower string // the same, lower-cased, for matching
}

// Section groups topics in the navigation column.
type Section struct {
	Title  string
	Topics []*Topic
}

// Result is one search hit.
type Result struct {
	Topic   *Topic
	Snippet string
	score   int
}

// Library is the loaded manual: every topic in every locale.
type Library struct {
	// locale → id → topic
	byLocale map[string]map[string]*Topic
	// route → topic id (locale-independent; routes are declared once, in en)
	routes map[string]string
	// every known topic id, in en's order
	ids []string
}

var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// Load reads every <root>/<locale>/<id>.md out of fsys.
//
// It fails rather than skipping on a malformed topic: a manual that silently
// drops a page is worse than one that won't build, because nobody notices
// until a shop owner goes looking for the page that isn't there.
func Load(fsys fs.FS, root string) (*Library, error) {
	lib := &Library{
		byLocale: map[string]map[string]*Topic{},
		routes:   map[string]string{},
	}
	routeOwner := map[string]string{} // route → file, for the error message

	locales, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("manual: reading %s: %w", root, err)
	}
	for _, ld := range locales {
		if !ld.IsDir() {
			continue
		}
		locale := ld.Name()
		if locale == "img" {
			// Generated screenshots (help/img/<locale>/<id>.png, written by
			// `make docs-shots`) live beside the locale dirs — not a locale.
			continue
		}
		dir := path.Join(root, locale)
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return nil, fmt.Errorf("manual: reading %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := path.Join(dir, e.Name())
			raw, err := fs.ReadFile(fsys, name)
			if err != nil {
				return nil, fmt.Errorf("manual: reading %s: %w", name, err)
			}
			tp, err := parseTopic(raw)
			if err != nil {
				return nil, fmt.Errorf("manual: %s: %w", name, err)
			}
			tp.Locale = locale
			tp.Translated = true
			// A generated screenshot (make docs-shots) leads the topic when
			// one exists for this locale. Checked against the same fsys the
			// topics come from — the embedded manual stays self-contained —
			// and injected here, at load time, so a topic served as an
			// English fallback carries the English screenshot along with the
			// English text rather than a broken locale-specific link. A
			// topic without one renders exactly as before: no placeholder,
			// no broken image.
			if _, statErr := fs.Stat(fsys, path.Join(root, "img", locale, tp.ID+".png")); statErr == nil {
				fig := fmt.Sprintf(
					`<figure class="manual-shot"><img src="/help/img/%s/%s.png" alt="%s" loading="lazy"></figure>`,
					locale, tp.ID, template.HTMLEscapeString(tp.Title),
				)
				tp.HTML = template.HTML(fig) + tp.HTML //nolint:gosec // locale/id are repo filenames, title is escaped
			}
			if want := strings.TrimSuffix(e.Name(), ".md"); tp.ID != want {
				return nil, fmt.Errorf("manual: %s: id %q does not match the filename", name, tp.ID)
			}
			if lib.byLocale[locale] == nil {
				lib.byLocale[locale] = map[string]*Topic{}
			}
			if _, dup := lib.byLocale[locale][tp.ID]; dup {
				return nil, fmt.Errorf("manual: %s: duplicate topic id %q", name, tp.ID)
			}
			lib.byLocale[locale][tp.ID] = tp

			// Routes are declared on the English topic; a translation
			// repeating them is fine, but must not contradict.
			for _, r := range tp.Routes {
				if owner, ok := lib.routes[r]; ok && owner != tp.ID {
					return nil, fmt.Errorf("manual: route %q claimed by both %q (%s) and %q (%s)",
						r, owner, routeOwner[r], tp.ID, name)
				}
				lib.routes[r] = tp.ID
				routeOwner[r] = name
			}
		}
	}

	base := lib.byLocale[FallbackLocale]
	if len(base) == 0 {
		return nil, fmt.Errorf("manual: no %s topics found under %s", FallbackLocale, root)
	}
	for id := range base {
		lib.ids = append(lib.ids, id)
	}
	sort.Strings(lib.ids)
	return lib, nil
}

// candidates expands a request locale into the directory names to try, most
// specific first.
//
// The till's own default locale is a BCP-47 tag ("en-US", from
// UT_DEFAULT_LOCALE) while the manual's directories are bare language codes
// ("en", "fa"). Without the base-subtag step every topic on a default install
// resolves as a fallback and the page stamps "not translated yet" across the
// entire English manual — which is what it did the first time this was opened
// in a browser.
func candidates(locale string) []string {
	if locale == "" {
		return nil
	}
	if base, _, found := strings.Cut(locale, "-"); found && base != "" {
		return []string{locale, base}
	}
	return []string{locale}
}

// Topic resolves a topic for a locale, falling back to English when it hasn't
// been translated. The returned Topic reports which locale it actually is.
func (l *Library) Topic(locale, id string) (*Topic, bool) {
	if strings.ContainsAny(id, "/\\.") {
		return nil, false // an id is a bare slug; anything else is a probe
	}
	for _, loc := range candidates(locale) {
		if tp, ok := l.byLocale[loc][id]; ok {
			return tp, true
		}
	}
	if tp, ok := l.byLocale[FallbackLocale][id]; ok {
		fb := *tp
		// English served for an English-ish request is not a fallback.
		fb.Translated = slices.Contains(candidates(locale), FallbackLocale)
		return &fb, true
	}
	return nil, false
}

// Tree is the navigation column for a locale: every topic the manual has,
// grouped into sections. Untranslated topics appear via their English
// fallback rather than disappearing from the list.
func (l *Library) Tree(locale string) []Section {
	bySection := map[string][]*Topic{}
	order := map[string]int{}
	for _, id := range l.ids {
		tp, ok := l.Topic(locale, id)
		if !ok {
			continue
		}
		sec := tp.Section
		if sec == "" {
			sec = tp.Title
		}
		bySection[sec] = append(bySection[sec], tp)
		if cur, seen := order[sec]; !seen || tp.Order < cur {
			order[sec] = tp.Order
		}
	}
	out := make([]Section, 0, len(bySection))
	for title, topics := range bySection {
		sort.SliceStable(topics, func(i, j int) bool {
			if topics[i].Order != topics[j].Order {
				return topics[i].Order < topics[j].Order
			}
			return topics[i].Title < topics[j].Title
		})
		out = append(out, Section{Title: title, Topics: topics})
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := order[out[i].Title], order[out[j].Title]
		if oi != oj {
			return oi < oj
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// TopicForRoute backs the contextual "?" — given the page the operator is
// looking at, which topic documents it.
func (l *Library) TopicForRoute(route string) (string, bool) {
	id, ok := l.routes[route]
	return id, ok
}

// IDs lists every topic id (English is the authoritative set).
func (l *Library) IDs() []string { return append([]string(nil), l.ids...) }

// Locales lists the locales that have at least one topic file.
func (l *Library) Locales() []string {
	out := make([]string, 0, len(l.byLocale))
	for loc := range l.byLocale {
		out = append(out, loc)
	}
	sort.Strings(out)
	return out
}

// MissingTranslations lists the topic ids a locale hasn't translated yet.
// The i18n card and the CI guard both read this.
func (l *Library) MissingTranslations(locale string) []string {
	var missing []string
	for _, id := range l.ids {
		if _, ok := l.byLocale[locale][id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

// Search scores title > keywords > summary > body. Deliberately simple: the
// corpus is a few dozen topics per locale, so an index would be more moving
// parts than the problem needs, and it stays correct as topics change.
func (l *Library) Search(locale, query string) []Result {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	terms := strings.Fields(q)
	var out []Result
	for _, id := range l.ids {
		tp, ok := l.Topic(locale, id)
		if !ok {
			continue
		}
		score := 0
		for _, term := range terms {
			switch {
			case strings.Contains(strings.ToLower(tp.Title), term):
				score += 100
			case containsFold(tp.Keywords, term):
				score += 50
			case strings.Contains(strings.ToLower(tp.Summary), term):
				score += 25
			case strings.Contains(tp.plainLower, term):
				score += 10
			}
		}
		if score == 0 {
			continue
		}
		out = append(out, Result{Topic: tp, Snippet: snippet(tp, terms), score: score})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].Topic.Title < out[j].Topic.Title
	})
	return out
}

func containsFold(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.Contains(strings.ToLower(h), needle) {
			return true
		}
	}
	return false
}

// snippet returns plain text around the first match — never markup, since it
// is rendered as text in the results list.
func snippet(tp *Topic, terms []string) string {
	body := tp.plain
	idx := -1
	for _, term := range terms {
		if i := strings.Index(tp.plainLower, term); i >= 0 && (idx < 0 || i < idx) {
			idx = i
		}
	}
	if idx < 0 {
		if tp.Summary != "" {
			return tp.Summary
		}
		idx = 0
	}
	start := max(idx-40, 0)
	end := min(start+160, len(body))
	s := strings.TrimSpace(body[start:end])
	if start > 0 {
		s = "…" + s
	}
	if end < len(body) {
		s += "…"
	}
	return s
}

// parseTopic splits the front matter from the Markdown body and renders it.
//
// The front matter is a deliberately tiny subset — "key: value" and
// "key: [a, b]" — rather than real YAML. It keeps the manual free of a YAML
// dependency, and the moment a topic needs more structure than this, that is
// a signal the manual is drifting toward being a CMS.
func parseTopic(raw []byte) (*Topic, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, fmt.Errorf("missing front matter")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, fmt.Errorf("unterminated front matter")
	}
	header := rest[:end]
	body := strings.TrimPrefix(rest[end+len("\n---"):], "\n")

	tp := &Topic{}
	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("bad front-matter line %q", line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "id":
			tp.ID = val
		case "title":
			tp.Title = unquote(val)
		case "section":
			tp.Section = unquote(val)
		case "summary":
			tp.Summary = unquote(val)
		case "order":
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("order %q is not a number", val)
			}
			tp.Order = n
		case "routes":
			tp.Routes = parseList(val)
		case "keywords":
			tp.Keywords = parseList(val)
		default:
			return nil, fmt.Errorf("unknown front-matter key %q", key)
		}
	}
	if tp.ID == "" {
		return nil, fmt.Errorf("front matter has no id")
	}
	if tp.Title == "" {
		return nil, fmt.Errorf("front matter has no title")
	}

	var buf bytes.Buffer
	if err := md.Convert([]byte(body), &buf); err != nil {
		return nil, fmt.Errorf("rendering markdown: %w", err)
	}
	tp.Markdown = body
	tp.HTML = template.HTML(buf.String()) //nolint:gosec // topics are repo content, not user input
	tp.plain = plainText(body)
	tp.plainLower = strings.ToLower(tp.plain)
	return tp, nil
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

func parseList(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = unquote(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// plainText strips the Markdown syntax that would otherwise pollute search
// matches and snippets (a query for "pay" shouldn't match "**Pay**" only
// because of the asterisks, and a snippet shouldn't show them).
func plainText(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "#>-* \t")
		if strings.HasPrefix(line, "![") {
			continue // a screenshot's alt text is not prose
		}
		line = strings.NewReplacer("**", "", "__", "", "`", "", "*", "", "_", "").Replace(line)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte(' ')
	}
	return b.String()
}
