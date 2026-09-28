// Package releasenotes is the till's built-in "What's new" (ut-docs#3091):
// short, shop-owner-language notes for each release, shown in Settings →
// About and announced once by a status-bar chip after an update.
//
// Notes are Markdown files under web/release-notes/<locale>/v<X.Y.Z>.md with
// a two-field front-matter (version, date), embedded into the binary — the
// same shape and the same offline reason as the manual (internal/manual,
// ADR-0003). English is the base: every release has an en note (release.yml
// refuses a tag without one), and a missing translation falls back to
// English per note, never to nothing.
package releasenotes

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/web"
)

// Root is the directory inside web.ReleaseNotesFS that holds the locales.
const Root = "release-notes"

// FallbackLocale is served when a note has no translation.
const FallbackLocale = "en"

// headingShift moves a note's "## New" down to <h5>: inside the About card
// the card title is h2, "What's new" h3 and each version h4 (settings.html).
const headingShift = 3

// Note is one release's notes in one locale.
type Note struct {
	Version string // "v0.30.6"
	Date    time.Time
	// Locale the text actually came from; Translated is false when this is
	// English standing in for a missing translation.
	Locale     string
	Translated bool
	Markdown   string
	HTML       template.HTML

	semver [3]int
}

// Library is every embedded note in every locale.
type Library struct {
	byLocale map[string]map[string]*Note // locale → version → note
	versions []string                    // en versions, newest first
}

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(shiftHeadings{}, 100))),
)

type shiftHeadings struct{}

func (shiftHeadings) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if h, ok := n.(*ast.Heading); ok && entering {
			h.Level = min(h.Level+headingShift, 6)
		}
		return ast.WalkContinue, nil
	})
}

// Tag normalizes a build version to its note filename stem: "0.30.6" and
// "v0.30.6" → "v0.30.6". Anything that is not a plain release
// (dev, a nightly, a pre-release) has no note and returns "".
func Tag(version string) string {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if _, ok := parseSemver(v); !ok {
		return ""
	}
	return "v" + v
}

func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || (len(p) > 1 && p[0] == '0') {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// semverLess reports a < b for two tags ("vX.Y.Z"); an unparsable tag
// sorts as the oldest.
func semverLess(a, b string) bool {
	x, _ := parseSemver(strings.TrimPrefix(a, "v"))
	y, _ := parseSemver(strings.TrimPrefix(b, "v"))
	return less(x, y)
}

// Load reads every <root>/<locale>/v<X.Y.Z>.md from fsys. Like the manual it
// fails on a malformed note rather than skipping it: a release whose notes
// silently vanish is worse than a build that refuses to start the tests.
func Load(fsys fs.FS, root string) (*Library, error) {
	lib := &Library{byLocale: map[string]map[string]*Note{}}
	locales, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("releasenotes: reading %s: %w", root, err)
	}
	for _, ld := range locales {
		if !ld.IsDir() {
			continue
		}
		locale := ld.Name()
		dir := path.Join(root, locale)
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return nil, fmt.Errorf("releasenotes: reading %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := path.Join(dir, e.Name())
			stem := strings.TrimSuffix(e.Name(), ".md")
			if !strings.HasPrefix(stem, "v") || Tag(stem) != stem {
				return nil, fmt.Errorf("releasenotes: %s: filename must be v<MAJOR>.<MINOR>.<PATCH>.md", name)
			}
			raw, err := fs.ReadFile(fsys, name)
			if err != nil {
				return nil, fmt.Errorf("releasenotes: reading %s: %w", name, err)
			}
			n, err := parseNote(raw)
			if err != nil {
				return nil, fmt.Errorf("releasenotes: %s: %w", name, err)
			}
			if n.Version != stem {
				return nil, fmt.Errorf("releasenotes: %s: version %q does not match the filename", name, n.Version)
			}
			n.Locale, n.Translated = locale, true
			if lib.byLocale[locale] == nil {
				lib.byLocale[locale] = map[string]*Note{}
			}
			lib.byLocale[locale][n.Version] = n
		}
	}
	for locale, notes := range lib.byLocale {
		if locale == FallbackLocale {
			continue
		}
		for v := range notes {
			if _, ok := lib.byLocale[FallbackLocale][v]; !ok {
				return nil, fmt.Errorf("releasenotes: %s/%s.md has no English (%s) original", locale, v, FallbackLocale)
			}
		}
	}
	en := lib.byLocale[FallbackLocale]
	for v := range en {
		lib.versions = append(lib.versions, v)
	}
	sort.Slice(lib.versions, func(i, j int) bool {
		return less(en[lib.versions[j]].semver, en[lib.versions[i]].semver)
	})
	return lib, nil
}

func parseNote(raw []byte) (*Note, error) {
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil, fmt.Errorf("missing front-matter (--- version/date ---)")
	}
	head, body, ok := strings.Cut(s[len("---\n"):], "\n---\n")
	if !ok {
		return nil, fmt.Errorf("unterminated front-matter")
	}
	n := &Note{}
	var date string
	for _, line := range strings.Split(head, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "version":
			n.Version = strings.TrimSpace(v)
		case "date":
			date = strings.TrimSpace(v)
		}
	}
	sv, ok := parseSemver(strings.TrimPrefix(n.Version, "v"))
	if !ok || !strings.HasPrefix(n.Version, "v") {
		return nil, fmt.Errorf("front-matter version %q must be v<MAJOR>.<MINOR>.<PATCH>", n.Version)
	}
	n.semver = sv
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return nil, fmt.Errorf("front-matter date %q must be YYYY-MM-DD", date)
	}
	n.Date = d
	n.Markdown = strings.TrimSpace(body)
	if n.Markdown == "" {
		return nil, fmt.Errorf("empty note")
	}
	var buf bytes.Buffer
	if err := md.Convert([]byte(n.Markdown), &buf); err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	// goldmark's default renderer drops raw HTML ("<!-- raw HTML omitted -->"),
	// so the output is safe to mark as trusted.
	n.HTML = template.HTML(buf.String())
	return n, nil
}

// Has reports whether version (with or without the "v") has an English note.
func (l *Library) Has(version string) bool {
	tag := Tag(version)
	if tag == "" {
		return false
	}
	_, ok := l.byLocale[FallbackLocale][tag]
	return ok
}

// Get returns version's note in locale, falling back to English. A region
// tag ("de-DE") resolves to its base language.
func (l *Library) Get(locale, version string) (*Note, bool) {
	tag := Tag(version)
	if tag == "" {
		return nil, false
	}
	for _, loc := range candidates(locale) {
		if n, ok := l.byLocale[loc][tag]; ok {
			return n, true
		}
	}
	en, ok := l.byLocale[FallbackLocale][tag]
	if !ok {
		return nil, false
	}
	fb := *en
	fb.Translated = false // English standing in for this locale
	return &fb, true
}

func candidates(locale string) []string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if locale == "" {
		return []string{FallbackLocale}
	}
	out := []string{locale}
	if i := strings.IndexAny(locale, "-_"); i > 0 {
		out = append(out, locale[:i])
	}
	return out
}

// Recent returns up to limit notes in locale, newest first, starting at the
// running version's own note. A build without a note of its own (dev, a
// nightly) gets the newest notes instead.
func (l *Library) Recent(locale, running string, limit int) []*Note {
	start := 0
	if tag := Tag(running); tag != "" {
		// versions is newest first: start at the running version's note, or
		// at the newest note older than it when it has none of its own — a
		// till never lists notes for a version it is not running yet.
		start = len(l.versions)
		for i, v := range l.versions {
			if !semverLess(tag, v) {
				start = i
				break
			}
		}
	}
	var out []*Note
	for _, v := range l.versions[start:] {
		if len(out) >= limit {
			break
		}
		if n, ok := l.Get(locale, v); ok {
			out = append(out, n)
		}
	}
	return out
}

var (
	builtinOnce sync.Once
	builtin     *Library
)

// Builtin is the library embedded in this binary, loaded once. A malformed
// embedded note is caught by TestBuiltin_NotesAreOwnerLanguage long before a
// release; at runtime it degrades to an empty library, never a crash.
func Builtin() *Library {
	builtinOnce.Do(func() {
		var err error
		builtin, err = Load(web.ReleaseNotesFS, Root)
		if err != nil {
			logging.L().Errorf("release notes: %v", err)
			builtin = &Library{byLocale: map[string]map[string]*Note{}}
		}
	})
	return builtin
}

var notice atomic.Value // string

// NoticeVersion is the version the after-update chip announces ("v0.30.6"),
// or "" when there is nothing to announce. Published at boot and cleared
// when a manager dismisses the chip or opens the notes (internal/pages).
func NoticeVersion() string {
	v, _ := notice.Load().(string)
	return v
}

// SetNoticeVersion publishes (or, with "", clears) the chip's version.
func SetNoticeVersion(v string) { notice.Store(v) }
