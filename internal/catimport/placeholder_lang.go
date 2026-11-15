package catimport

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Shop-language icon suggestion (ut-docs#2654). The English keywordIcons
// table stays the last resort; before it, an item's name (then category) is
// matched against the translated tile labels (catalog.builtin_icon.<key>)
// of each of the shop's languages. The words come from the normal i18n
// layers (built-in file, language pack/bundle, shop override), so core
// carries no non-English data (ADR-0160) — only language-neutral
// normalisation (placeholder_norm.go).
//
// Language facts (as opposed to translations) travel in the string layer too
// (ADR-0160 §12, ut-docs#6043): a language whose translation sets
// catalog.icon_match.head_last to "1" has compounds whose main noun comes
// last ("Milchkaffee"), and its pass then also matches a label at the end of
// a longer word. Core holds no per-language list (#2848).

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
	english  bool
	rows     []labelRow
	headLast bool // compounds end in their main noun (headLastKey is "1")
}

// headLastKey is the i18n setting that turns head-noun-last matching on for a
// language. Only the exact value "1" (trimmed) enables it; anything else,
// including a missing key, keeps the default behaviour.
const headLastKey = "catalog.icon_match.head_last"

// Head-noun step thresholds, in runes (script-neutral): the label must be at
// least headMinLabelRunes long and the word must carry at least
// headMinModifierRunes more in front of it ("Rotwein", modifier 3, stays
// unmatched; "Schwein" no longer reaches "wein").
const (
	headMinLabelRunes    = 3
	headMinModifierRunes = 4
)

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
			hl, _ := lookup(loc, headLastKey)
			m.passes = append(m.passes, langPass{rows: rows, headLast: strings.TrimSpace(hl) == "1"})
		}
	}
	return m
}

func runeLen(s string) int { return len([]rune(s)) }

// Icon picks an icon key in tiers: a strict match on the name across all
// passes (each shop language in order, English at its own position, else
// last), then the same on the category, then — only for head-last languages —
// the head-noun step on the name, then on the category, then "generic". A weak
// head-noun hit never beats a strict hit from any language or the category.
func (m *IconMatcher) Icon(name, category string) string {
	if icon, ok := m.match(name, false); ok {
		return icon
	}
	if icon, ok := m.match(category, false); ok {
		return icon
	}
	if icon, ok := m.match(name, true); ok {
		return icon
	}
	if icon, ok := m.match(category, true); ok {
		return icon
	}
	return "generic"
}

// IconPath is Icon plus IconPath: a public asset path ready to store.
func (m *IconMatcher) IconPath(name, category string) string {
	path, _ := IconPath(m.Icon(name, category))
	return path
}

// nameView is one normalised reading of an item name: its words joined for
// phrase matching, and the tokens tried against single-word labels.
type nameView struct {
	joined string
	tokens []string
}

// matchRows is one non-English pass's strict step over the views: phrase,
// exact, short-suffix prefix.
func (p langPass) matchRows(views []nameView) (string, bool) {
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
	return "", false
}

// matchHead is a head-last pass's head-noun step over the views.
func (p langPass) matchHead(views []nameView) (string, bool) {
	if !p.headLast {
		return "", false
	}
	// Rows are longest first, so the first hit is the longest label.
	for _, r := range p.rows {
		if len(r.words) != 1 {
			continue
		}
		for _, v := range views {
			for _, t := range v.tokens {
				if headWordMatches(r.words[0], t) {
					return r.icon, true
				}
			}
		}
	}
	return "", false
}

// match runs one tier over s: head=false is the strict tier (every pass, then
// the trailing English table when no pass was English); head=true is the
// head-noun tier, which only head-last passes take part in.
func (m *IconMatcher) match(s string, head bool) (string, bool) {
	if strings.TrimSpace(s) == "" {
		return "", false
	}
	if m == nil || len(m.passes) == 0 {
		if head {
			return "", false
		}
		return matchKeyword(s)
	}
	if head && !m.anyHeadLast() {
		return "", false
	}
	// Views of the name: the ZWNJ-split form, plus the joined form when the
	// name has a ZWNJ (ut-docs#6052). Either may hit a row.
	mk := func(words []string) nameView {
		// Tokens to try for single-word labels: each word, plus the word
		// with an Arabic article stripped.
		tokens := make([]string, 0, len(words)*2)
		for _, w := range words {
			tokens = append(tokens, w)
			if st, ok := stripArticle(w); ok {
				tokens = append(tokens, st)
			}
		}
		return nameView{joined: " " + strings.Join(words, " ") + " ", tokens: tokens}
	}
	views := []nameView{mk(normalizedWords(s))}
	if hasZWNJ(s) {
		views = append(views, mk(joinedWords(s)))
	}
	englishDone := false
	for _, p := range m.passes {
		if head {
			if icon, ok := p.matchHead(views); ok {
				return icon, true
			}
			continue
		}
		if p.english {
			englishDone = true
			if icon, ok := matchKeyword(s); ok {
				return icon, true
			}
			continue
		}
		if icon, ok := p.matchRows(views); ok {
			return icon, true
		}
	}
	if head || englishDone {
		return "", false
	}
	return matchKeyword(s)
}

func (m *IconMatcher) anyHeadLast() bool {
	for _, p := range m.passes {
		if p.headLast {
			return true
		}
	}
	return false
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

// headWordMatches: the token ends in the label (a compound whose main noun
// comes last, "milchkaffee" -> "kaffee"), the label is at least
// headMinLabelRunes long and at least headMinModifierRunes precede it.
func headWordMatches(label, tok string) bool {
	if !strings.HasSuffix(tok, label) {
		return false
	}
	lr, tr := utf8.RuneCountInString(label), utf8.RuneCountInString(tok)
	return lr >= headMinLabelRunes && tr-lr >= headMinModifierRunes
}
