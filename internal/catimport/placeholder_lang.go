package catimport

import (
	"sort"
	"strings"
)

// Shop-language icon suggestion (ut-docs#2654). The English keywordIcons
// table stays the last resort; before it, an item's name (then category) is
// matched against the translated tile labels (catalog.builtin_icon.<key>)
// of each of the shop's languages. The words come from the normal i18n
// layers (built-in file, language pack/bundle, shop override), so core
// carries no non-English data (ADR-0160) — only language-neutral
// normalisation (placeholder_norm.go).

// LabelLookup returns the exact-locale translation of an i18n key, with no
// fallback to another language and no key echo.
type LabelLookup func(locale, key string) (string, bool)

type labelRow struct {
	icon   string
	words  []string // normalised label words (len >= 1)
	joined string   // " w1 w2 " for adjacent multi-word matching
}

// langPass is one language's matching step: its normalised label rows, or
// (english) the Go keyword table.
type langPass struct {
	english bool
	rows    []labelRow
}

// IconMatcher suggests icons using the shop's languages. A nil matcher is
// valid and behaves as English only.
type IconMatcher struct {
	passes []langPass
}

// NewIconMatcher builds a matcher for locales (shop default first). An "en"
// entry is the Go keyword table (core ships English, ADR-0160), checked at
// that position, so an English-default shop checks English before its other
// staff languages; without one, English runs last. Labels are normalised
// once, here.
func NewIconMatcher(locales []string, lookup LabelLookup) *IconMatcher {
	m := &IconMatcher{}
	if lookup == nil {
		return m
	}
	seen := map[string]bool{}
	english := false
	for _, loc := range locales {
		base := strings.ToLower(loc)
		if i := strings.IndexAny(base, "-_"); i > 0 {
			base = base[:i]
		}
		if base == "" || seen[strings.ToLower(loc)] {
			continue
		}
		seen[strings.ToLower(loc)] = true
		if base == "en" {
			if !english {
				english = true
				m.passes = append(m.passes, langPass{english: true})
			}
			continue
		}
		var rows []labelRow
		for _, d := range iconDefs {
			if d.Key == "generic" {
				continue
			}
			label, ok := lookup(loc, "catalog.builtin_icon."+d.Key)
			if !ok || strings.TrimSpace(label) == "" {
				continue
			}
			for _, alt := range strings.FieldsFunc(label, func(r rune) bool { return r == '/' || r == ',' }) {
				w := normalizedWords(alt)
				if len(w) == 0 {
					continue
				}
				rows = append(rows, labelRow{icon: d.Key, words: w, joined: " " + strings.Join(w, " ") + " "})
				if hasZWNJ(alt) {
					if jw := joinedWords(alt); len(jw) > 0 && strings.Join(jw, " ") != strings.Join(w, " ") {
						rows = append(rows, labelRow{icon: d.Key, words: jw, joined: " " + strings.Join(jw, " ") + " "})
					}
				}
			}
		}
		sort.SliceStable(rows, func(i, j int) bool {
			if len(rows[i].words) != len(rows[j].words) {
				return len(rows[i].words) > len(rows[j].words)
			}
			return runeLen(rows[i].joined) > runeLen(rows[j].joined)
		})
		if len(rows) > 0 {
			m.passes = append(m.passes, langPass{rows: rows})
		}
	}
	return m
}

func runeLen(s string) int { return len([]rune(s)) }

// Icon picks an icon key: the name in each shop language in order (English
// at its own position, else last), then the same for the category, then
// "generic".
func (m *IconMatcher) Icon(name, category string) string {
	if icon, ok := m.match(name); ok {
		return icon
	}
	if icon, ok := m.match(category); ok {
		return icon
	}
	return "generic"
}

// IconPath is Icon plus IconPath: a public asset path ready to store.
func (m *IconMatcher) IconPath(name, category string) string {
	path, _ := IconPath(m.Icon(name, category))
	return path
}

func (m *IconMatcher) match(s string) (string, bool) {
	if strings.TrimSpace(s) == "" {
		return "", false
	}
	if m == nil || len(m.passes) == 0 {
		return matchKeyword(s)
	}
	// Views of the name: the ZWNJ-split form, plus the joined form when the
	// name has a ZWNJ (ut-docs#6052). Either may hit a row.
	type view struct {
		joined string
		tokens []string
	}
	mk := func(words []string) view {
		// Tokens to try for single-word labels: each word, plus the word
		// with an Arabic article stripped.
		tokens := make([]string, 0, len(words)*2)
		for _, w := range words {
			tokens = append(tokens, w)
			if st, ok := stripArticle(w); ok {
				tokens = append(tokens, st)
			}
		}
		return view{joined: " " + strings.Join(words, " ") + " ", tokens: tokens}
	}
	views := []view{mk(normalizedWords(s))}
	if hasZWNJ(s) {
		views = append(views, mk(joinedWords(s)))
	}
	englishDone := false
	for _, p := range m.passes {
		if p.english {
			englishDone = true
			if icon, ok := matchKeyword(s); ok {
				return icon, true
			}
			continue
		}
		for _, r := range p.rows {
			for _, v := range views {
				if len(r.words) > 1 {
					if strings.Contains(v.joined, r.joined) {
						return r.icon, true
					}
					continue
				}
				for _, t := range v.tokens {
					if labelWordMatches(r.words[0], t) {
						return r.icon, true
					}
				}
			}
		}
	}
	if englishDone {
		return "", false
	}
	return matchKeyword(s)
}

// labelWordMatches: exact, or (label of >= 4 runes) token starting with the
// label plus at most 3 more runes — a suffix inflection ("kahvesi", "biere"),
// never an infix or a long compound ("bierglas", "eistee", "aguacate").
func labelWordMatches(label, tok string) bool {
	if tok == label {
		return true
	}
	lr, tr := runeLen(label), runeLen(tok)
	return lr >= 4 && tr > lr && tr-lr <= 3 && strings.HasPrefix(tok, label)
}
