package help

import "strings"

// maxResults caps a search so a one-letter query can't dump the whole manual.
const maxResults = 20

// Search matches query against lang's topics (English set when the language
// has none of its own), case-insensitively. Rank tiers: exact title match,
// title substring, keyword match, body substring. Ties keep tree order. An
// empty query returns nothing — the caller restores the full tree instead.
func (idx *Index) Search(lang, query string) []*Topic {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	type scored struct {
		t    *Topic
		rank int
	}
	var matches []scored
	// Walk the section tree (not the map) so tie order is deterministic.
	for _, sec := range idx.Sections(lang) {
		for _, t := range sec.Topics {
			if r, ok := rank(t, q); ok {
				matches = append(matches, scored{t, r})
			}
		}
	}
	// Stable selection sort by rank keeps tree order within a tier without
	// pulling in sort.SliceStable's index gymnastics for a tiny slice.
	out := make([]*Topic, 0, len(matches))
	for tier := 0; tier <= 3 && len(out) < maxResults; tier++ {
		for _, m := range matches {
			if m.rank == tier {
				out = append(out, m.t)
				if len(out) == maxResults {
					break
				}
			}
		}
	}
	return out
}

// rank scores a topic against a lowercased query; ok=false means no match.
func rank(t *Topic, q string) (int, bool) {
	title := strings.ToLower(t.Title)
	switch {
	case title == q:
		return 0, true
	case strings.Contains(title, q):
		return 1, true
	}
	for _, k := range t.Keywords {
		if strings.Contains(strings.ToLower(k), q) {
			return 2, true
		}
	}
	if strings.Contains(strings.ToLower(t.Body), q) {
		return 3, true
	}
	return 0, false
}
