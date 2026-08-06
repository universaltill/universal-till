package help

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"gopkg.in/yaml.v3"
)

// frontMatter is the YAML block between the leading "---" fences of a topic
// file. id, title and section are required; the rest defaults sensibly.
type frontMatter struct {
	ID       string   `yaml:"id"`
	Title    string   `yaml:"title"`
	Section  string   `yaml:"section"`
	Order    int      `yaml:"order"`
	Routes   []string `yaml:"routes"`
	Keywords []string `yaml:"keywords"`
}

// markdown renders CommonMark + GFM (tables, strikethrough, autolinks).
// Raw HTML in the source is escaped by goldmark's default (safe) renderer —
// see the sanitizer TODO on Topic.HTML for the day plugins author topics.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// LoadTopics walks <lang>/*.md under fsys (the embedded web/help tree),
// parses each file's front matter and renders its Markdown body. It fails
// loudly — a malformed file, a missing required field or a duplicate id
// within a language is a startup error, not a silent request-time gap.
func LoadTopics(fsys fs.FS) (*Index, error) {
	idx := &Index{
		BySection: map[string][]Section{},
		ByID:      map[string]map[string]*Topic{},
	}
	langs, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("help: read topic root: %w", err)
	}
	for _, langDir := range langs {
		if !langDir.IsDir() {
			continue
		}
		lang := langDir.Name()
		files, err := fs.ReadDir(fsys, lang)
		if err != nil {
			return nil, fmt.Errorf("help: read %s: %w", lang, err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			path := lang + "/" + f.Name()
			raw, err := fs.ReadFile(fsys, path)
			if err != nil {
				return nil, fmt.Errorf("help: read %s: %w", path, err)
			}
			topic, err := parseTopic(raw, lang)
			if err != nil {
				return nil, fmt.Errorf("help: %s: %w", path, err)
			}
			if idx.ByID[lang] == nil {
				idx.ByID[lang] = map[string]*Topic{}
			}
			if _, dup := idx.ByID[lang][topic.ID]; dup {
				return nil, fmt.Errorf("help: %s: duplicate topic id %q in %s", path, topic.ID, lang)
			}
			idx.ByID[lang][topic.ID] = topic
		}
	}
	for lang, topics := range idx.ByID {
		idx.BySection[lang] = buildSections(topics)
	}
	return idx, nil
}

// parseTopic splits the front matter from the Markdown body and renders it.
func parseTopic(raw []byte, lang string) (*Topic, error) {
	fmBytes, body, err := splitFrontMatter(raw)
	if err != nil {
		return nil, err
	}
	var fm frontMatter
	if err := yaml.Unmarshal(fmBytes, &fm); err != nil {
		return nil, fmt.Errorf("front matter: %w", err)
	}
	switch {
	case strings.TrimSpace(fm.ID) == "":
		return nil, fmt.Errorf("front matter: missing required field %q", "id")
	case strings.TrimSpace(fm.Title) == "":
		return nil, fmt.Errorf("front matter: missing required field %q", "title")
	case strings.TrimSpace(fm.Section) == "":
		return nil, fmt.Errorf("front matter: missing required field %q", "section")
	}
	var buf bytes.Buffer
	if err := markdown.Convert(body, &buf); err != nil {
		return nil, fmt.Errorf("render markdown: %w", err)
	}
	return &Topic{
		ID:         strings.TrimSpace(fm.ID),
		Lang:       lang,
		Title:      fm.Title,
		Section:    fm.Section,
		Order:      fm.Order,
		Routes:     fm.Routes,
		Keywords:   fm.Keywords,
		Body:       string(body),
		HTML:       template.HTML(buf.String()), //nolint:gosec // dev-authored trusted content; goldmark escapes raw HTML
		Translated: true,
	}, nil
}

// splitFrontMatter returns the YAML between the leading "---" fences and the
// remaining Markdown body. Both fences are required.
func splitFrontMatter(raw []byte) (fm, body []byte, err error) {
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	const fence = "---\n"
	if !strings.HasPrefix(content, fence) {
		return nil, nil, fmt.Errorf("front matter: file must start with %q", "---")
	}
	rest := content[len(fence):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		// allow a file that is nothing but front matter ending in "---"
		if trimmed, ok := strings.CutSuffix(rest, "\n---"); ok {
			return []byte(trimmed), nil, nil
		}
		return nil, nil, fmt.Errorf("front matter: closing %q fence not found", "---")
	}
	return []byte(rest[:end]), []byte(rest[end+len("\n---\n"):]), nil
}

// buildSections groups a language's topics into the ordered tree: topics
// within a section by (Order, Title); sections by their smallest topic Order,
// then name — so section order is driven entirely by the files' front matter.
func buildSections(topics map[string]*Topic) []Section {
	bySection := map[string][]*Topic{}
	for _, t := range topics {
		bySection[t.Section] = append(bySection[t.Section], t)
	}
	sections := make([]Section, 0, len(bySection))
	for name, ts := range bySection {
		sort.Slice(ts, func(i, j int) bool {
			if ts[i].Order != ts[j].Order {
				return ts[i].Order < ts[j].Order
			}
			return ts[i].Title < ts[j].Title
		})
		sections = append(sections, Section{Name: name, Topics: ts})
	}
	sort.Slice(sections, func(i, j int) bool {
		oi, oj := sections[i].Topics[0].Order, sections[j].Topics[0].Order
		if oi != oj {
			return oi < oj
		}
		return sections[i].Name < sections[j].Name
	})
	return sections
}
