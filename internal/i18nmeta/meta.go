// Package i18nmeta reads the till's translation key metadata
// (web/i18n-meta: en.meta.json + areas.json, ADR-0160 §2/§3) and builds the
// Weblate link for one key, for translate mode's key panel (ADR-0160 §7,
// ut-docs#4370). Area resolution mirrors ut-translations'
// tools/i18n_map.py area_for(), which is what names the Weblate
// components: an explicit, known `area`, else the area whose namespaces
// list the key's first segment (first listed wins), else default_area.
//
// The till only builds links: it holds no Weblate token and never calls
// Weblate (ADR-0160 §7).
package i18nmeta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	metafs "github.com/universaltill/universal-till/web/i18n-meta"
)

// WeblateBase and WeblateProject locate the shared Weblate instance and the
// till's project on it (ADR-0160 §1, #4364).
const (
	WeblateBase    = "https://translate.universaltill.com"
	WeblateProject = "universal-till"
)

// Key is one key's metadata. Only Description and Review are guaranteed
// (guard-i18n-meta.sh); most pre-ADR keys have no entry at all yet.
type Key struct {
	Description  string   `json:"description"`
	Placeholders []string `json:"placeholders"`
	MaxLength    int      `json:"max_length"`
	Area         string   `json:"area"`
	Review       string   `json:"review"`
}

// Catalog is the parsed metadata.
type Catalog struct {
	keys        map[string]Key
	nsArea      map[string]string
	areas       map[string]bool
	defaultArea string
}

var (
	builtinOnce sync.Once
	builtin     *Catalog
	builtinErr  error
)

// Builtin is the catalog embedded in the binary, parsed once.
func Builtin() (*Catalog, error) {
	builtinOnce.Do(func() {
		meta, err := metafs.FS.ReadFile("en.meta.json")
		if err != nil {
			builtinErr = err
			return
		}
		areas, err := metafs.FS.ReadFile("areas.json")
		if err != nil {
			builtinErr = err
			return
		}
		builtin, builtinErr = parse(meta, areas)
	})
	return builtin, builtinErr
}

func parse(meta, areasDoc []byte) (*Catalog, error) {
	c := &Catalog{keys: map[string]Key{}, nsArea: map[string]string{}, areas: map[string]bool{}}
	if err := json.Unmarshal(meta, &c.keys); err != nil {
		return nil, fmt.Errorf("en.meta.json: %w", err)
	}
	var doc struct {
		DefaultArea string          `json:"default_area"`
		Areas       json.RawMessage `json:"areas"`
	}
	if err := json.Unmarshal(areasDoc, &doc); err != nil {
		return nil, fmt.Errorf("areas.json: %w", err)
	}
	c.defaultArea = doc.DefaultArea
	if c.defaultArea == "" {
		c.defaultArea = "common"
	}
	c.areas[c.defaultArea] = true
	// Walk "areas" in document order: a namespace listed by two areas goes
	// to the first, exactly as i18n_map.namespace_index's setdefault does.
	dec := json.NewDecoder(bytes.NewReader(doc.Areas))
	if _, err := dec.Token(); err != nil { // {
		return nil, fmt.Errorf("areas.json areas: %w", err)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("areas.json areas: %w", err)
		}
		name, _ := tok.(string)
		var spec struct {
			Namespaces []string `json:"namespaces"`
		}
		if err := dec.Decode(&spec); err != nil {
			return nil, fmt.Errorf("areas.json area %q: %w", name, err)
		}
		c.areas[name] = true
		for _, ns := range spec.Namespaces {
			if _, ok := c.nsArea[ns]; !ok {
				c.nsArea[ns] = name
			}
		}
	}
	return c, nil
}

// Lookup returns key's metadata, if it has any.
func (c *Catalog) Lookup(key string) (Key, bool) {
	k, ok := c.keys[key]
	return k, ok
}

// AreaFor is the Weblate component (area id) key lands in.
func (c *Catalog) AreaFor(key string) string {
	if k, ok := c.keys[key]; ok && k.Area != "" && c.areas[k.Area] {
		return k.Area
	}
	ns, _, _ := strings.Cut(key, ".")
	if a, ok := c.nsArea[ns]; ok {
		return a
	}
	return c.defaultArea
}

// WeblateLanguage maps a till locale tag ("de", "pt-BR", "sr-latn") to
// Weblate's language code ("de", "pt_BR", "sr_Latn"). Empty means English,
// the till's base locale.
func WeblateLanguage(locale string) string {
	parts := strings.FieldsFunc(locale, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) == 0 {
		return "en"
	}
	out := []string{strings.ToLower(parts[0])}
	for _, p := range parts[1:] {
		switch len(p) {
		case 4: // a script subtag
			out = append(out, strings.ToUpper(p[:1])+strings.ToLower(p[1:]))
		default: // a region subtag
			out = append(out, strings.ToUpper(p))
		}
	}
	return strings.Join(out, "_")
}

// WeblateURL opens key in area's component, in locale's translation, using
// Weblate's exact-key search (`key:="..."`, checked against Weblate
// 2026.10 on translate.universaltill.com). The till never fetches it.
func WeblateURL(area, locale, key string) string {
	q := `key:="` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(key) + `"`
	return WeblateBase + "/translate/" + url.PathEscape(WeblateProject) + "/" +
		url.PathEscape(area) + "/" + url.PathEscape(WeblateLanguage(locale)) + "/?q=" + url.QueryEscape(q)
}
