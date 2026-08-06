// Package help builds the in-memory index behind the /help user manual
// (ut-docs#325): Markdown topic files with YAML front matter, embedded under
// web/help/<lang>/*.md, loaded once at startup into a section tree with
// per-language lookup (English fallback) and in-memory search.
package help

import "html/template"

// Topic is one manual page, parsed from <lang>/<file>.md.
type Topic struct {
	ID      string
	Lang    string
	Title   string
	Section string
	Order   int
	// Routes lists app routes this topic documents — reserved for the
	// contextual "?" links and the guard-help-topics CI check (ut-docs#326);
	// loaded but unused in this card.
	Routes   []string
	Keywords []string
	// Body is the raw Markdown source (searchable text).
	Body string
	// HTML is the rendered body. Topics are dev-authored, trusted content
	// (goldmark escapes raw HTML in the source by default). TODO(ut-docs#326+):
	// if plugins ever contribute manual topics, add an HTML sanitizer before
	// trusting their output as template.HTML.
	HTML template.HTML
	// Translated is false when this topic is being served as an English
	// fallback for a locale that has no translation yet — the template shows
	// a "not yet translated" banner (topic translation is backlog ut-docs#341).
	Translated bool
}

// Section groups topics for the tree, ordered by Order then Title.
type Section struct {
	Name   string
	Topics []*Topic
}

// Index is the immutable, load-once topic index the /help handlers close over.
type Index struct {
	// BySection holds each language's ordered section tree.
	BySection map[string][]Section
	// ByID holds lang → topic id → topic.
	ByID map[string]map[string]*Topic
}
