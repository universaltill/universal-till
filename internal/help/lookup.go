package help

// fallbackLang is the authoritative manual language — every topic exists in
// English first; other languages fill in over time (backlog ut-docs#341).
const fallbackLang = "en"

// Get looks up a topic for lang, falling back to English. translated is
// false when the English fallback served (the caller shows a "not yet
// translated" banner); a topic missing in English too returns (nil, false).
func (idx *Index) Get(lang, id string) (topic *Topic, translated bool) {
	if t, ok := idx.ByID[lang][id]; ok {
		return t, true
	}
	if t, ok := idx.ByID[fallbackLang][id]; ok {
		return t, false
	}
	return nil, false
}

// Sections returns lang's ordered section tree, or the English tree when the
// language has no topics of its own at all yet.
func (idx *Index) Sections(lang string) []Section {
	if secs := idx.BySection[lang]; len(secs) > 0 {
		return secs
	}
	return idx.BySection[fallbackLang]
}
