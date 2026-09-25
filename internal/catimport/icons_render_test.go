package catimport

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// The category icon tile generator (ut-docs#2506). It lives in a _test.go
// file on purpose: the till binary only serves the committed tiles, so the
// renderer is test/build-time code (guard-deadcode-baseline.sh would flag
// it as unreachable in the shipped binary). TestCategoryIconTiles_InSync
// (icons_test.go) compares every committed tile with this output and, with
// UPDATE_CATEGORY_ICONS=1, rewrites them.

// iconGroupFor returns the group definition for key (it always exists:
// TestIconRegistry_Consistent pins every def's Group to iconGroups).
func iconGroupFor(key string) iconGroup {
	for _, g := range iconGroups {
		if g.Key == key {
			return g
		}
	}
	return iconGroups[len(iconGroups)-1]
}

var (
	svgOpenTag   = regexp.MustCompile(`(?s)<svg\b[^>]*>`)
	svgComment   = regexp.MustCompile(`(?s)<!--.*?-->`)
	svgElement   = regexp.MustCompile(`(?s)<[a-z]+\b[^>]*/>`)
	tablerFiller = regexp.MustCompile(`stroke="none"`)
	spaceRun     = regexp.MustCompile(`\s+`)
)

// iconSourceChildren extracts the drawing elements from one upstream
// 24×24 SVG: every self-closing child element, whitespace-normalised,
// minus Tabler's invisible bounding-box path (stroke="none"). Upstream
// SVGs only ever use self-closing children; one that doesn't yields no
// elements and the generator refuses it rather than emit an empty tile.
func iconSourceChildren(src []byte) ([]string, error) {
	s := svgComment.ReplaceAllString(string(src), "")
	loc := svgOpenTag.FindStringIndex(s)
	end := strings.LastIndex(s, "</svg>")
	if loc == nil || end < loc[1] {
		return nil, fmt.Errorf("not an <svg> document")
	}
	body := s[loc[1]:end]
	var out []string
	for _, el := range svgElement.FindAllString(body, -1) {
		if tablerFiller.MatchString(el) {
			continue
		}
		el = spaceRun.ReplaceAllString(el, " ")
		el = strings.Replace(el, " />", "/>", 1)
		out = append(out, el)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no drawable elements")
	}
	return out, nil
}

// renderIconTile builds the 64×64 tile for d from its upstream source.
// The 24-unit glyph is scaled to 40 units and centred (12-unit margin),
// on the group's background, stroked in the group's colour.
func renderIconTile(d iconDef, src []byte) ([]byte, error) {
	children, err := iconSourceChildren(src)
	if err != nil {
		return nil, fmt.Errorf("%s (%s): %w", d.Key, d.Src, err)
	}
	g := iconGroupFor(d.Group)
	set := strings.SplitN(d.Src, ":", 2)[0]
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">` + "\n")
	fmt.Fprintf(&b, "  <!-- %s, generated from internal/catimport/icons.go (ut-docs#2506). Licence: internal/catimport/iconsrc/%s/LICENSE -->\n", d.Src, set)
	fmt.Fprintf(&b, "  <rect width=\"64\" height=\"64\" fill=\"%s\"/>\n", g.Background)
	fmt.Fprintf(&b, "  <g transform=\"translate(12 12) scale(1.6667)\" fill=\"none\" stroke=\"%s\" stroke-width=\"2\" stroke-linecap=\"round\" stroke-linejoin=\"round\">\n", g.Stroke)
	// A filled dot (Lucide's tag hole) must take the tile's stroke colour;
	// currentColor would resolve to black inside an <img>.
	for _, el := range children {
		b.WriteString("    " + strings.ReplaceAll(el, "currentColor", g.Stroke) + "\n")
	}
	b.WriteString("  </g>\n</svg>\n")
	return []byte(b.String()), nil
}

// iconSourcePath is the vendored upstream file for src ("lucide:beer").
func iconSourcePath(srcDir, src string) string {
	parts := strings.SplitN(src, ":", 2)
	return filepath.Join(srcDir, parts[0], parts[1]+".svg")
}
