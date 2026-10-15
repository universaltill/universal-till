package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// ut-docs#4408: a text back arrow is a bidi-neutral glyph, so it points
// left even on an RTL page. Every "←" in a template must sit in
// <span class="back-arrow" aria-hidden="true">, which app.css mirrors under
// dir="rtl". Comments may mention the glyph.
func TestNoBareBackArrowGlyph(t *testing.T) {
	comment := regexp.MustCompile(`(?s)<!--.*?-->|\{\{/\*.*?\*/\}\}`)
	const wrapped = `<span class="back-arrow" aria-hidden="true">←</span>`
	err := fs.WalkDir(FS, "ui", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		b, err := fs.ReadFile(FS, p)
		if err != nil {
			return err
		}
		s := comment.ReplaceAllString(string(b), "")
		if n := strings.Count(strings.ReplaceAll(s, wrapped, ""), "←"); n > 0 {
			t.Errorf("%s: %d bare \"←\" (wrap it: %s)", p, n, wrapped)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
