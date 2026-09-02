package httpx

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ut-docs#1423: the rail's icons are inline SVG from one shared wrapper so
// that no icon can be sized differently from its siblings by the device's
// emoji font (which is what defeated #1332's and #1348's per-glyph fixes on
// the tablet).

func TestIconHTMLSharedWrapper(t *testing.T) {
	for _, name := range IconNames() {
		out := string(iconHTML(name))
		if !strings.HasPrefix(out, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"`) {
			t.Errorf("icon %q does not use the shared 24x24 stroke wrapper: %s", name, out)
		}
		if !strings.Contains(out, `aria-hidden="true"`) {
			t.Errorf("icon %q must be decorative (aria-hidden): the label span carries the name", name)
		}
		if !strings.HasSuffix(out, `</svg>`) {
			t.Errorf("icon %q is not closed: %s", name, out)
		}
		open := out[:strings.Index(out, ">")+1] // the shared <svg …> tag only
		if strings.Contains(open, ` width="`) || strings.Contains(open, ` height="`) {
			t.Errorf("icon %q carries its own width/height; size must come from CSS only: %s", name, open)
		}
	}
}

func TestIconHTMLUnknownRendersNothing(t *testing.T) {
	if got := iconHTML("no-such-icon"); got != "" {
		t.Fatalf("unknown icon must render empty, got %q", got)
	}
}

// Every {{ icon "x" }} in the UI templates must resolve, and the rail
// partials must not have slid back to emoji.
func TestRailIconsReferencedByTemplatesExist(t *testing.T) {
	root := filepath.Join("..", "..", "web", "ui")
	call := regexp.MustCompile(`\{\{\s*icon\s+"([^"]+)"\s*\}\}`)
	emoji := regexp.MustCompile(`class="nav-toggle-ico[^"]*"[^>]*>\s*[^<{\s]`)
	seen := 0
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range call.FindAllStringSubmatch(string(b), -1) {
			seen++
			if _, ok := railIcons[m[1]]; !ok {
				t.Errorf("%s references unknown icon %q (known: %v)", p, m[1], IconNames())
			}
		}
		if m := emoji.FindString(string(b)); m != "" {
			t.Errorf("%s: a .nav-toggle-ico span carries text/emoji content instead of {{ icon }}: %q", p, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 11 {
		t.Fatalf("expected the rail's 11 icons to be referenced via {{ icon }}, found %d", seen)
	}
}
