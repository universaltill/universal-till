package pages

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ut-docs#2702: removing a whole `@media { ... }` block from app.css left
// its closing brace behind. Browsers recover from a stray `}` silently by
// dropping the NEXT rule -- here `.pos-container > .basket { grid-area:
// basket; }`, so the basket stopped spanning both grid rows and lost a
// fifth of its height, with no console error and no failing Go test. Only a
// geometry e2e spec noticed. This makes the whole class a unit-test
// failure: braces in every first-party stylesheet must balance, and never
// go negative, once comments and strings are stripped.
func TestFirstPartyCSSBracesBalance(t *testing.T) {
	chdirRoot(t)
	files, err := filepath.Glob(filepath.Join("web", "public", "*.css"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no stylesheets found under web/public (err=%v)", err)
	}
	comments := regexp.MustCompile(`(?s)/\*.*?\*/`)
	stringsRe := regexp.MustCompile(`"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'`)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		// Keep newlines inside comments so reported line numbers stay true.
		src := comments.ReplaceAllStringFunc(string(raw), func(c string) string {
			return strings.Repeat("\n", strings.Count(c, "\n"))
		})
		src = stringsRe.ReplaceAllString(src, `""`)
		depth, line := 0, 1
		for _, c := range src {
			switch c {
			case '\n':
				line++
			case '{':
				depth++
			case '}':
				depth--
				if depth < 0 {
					t.Fatalf("%s:%d: unmatched `}` -- the browser silently drops the rule after it", f, line)
				}
			}
		}
		if depth != 0 {
			t.Fatalf("%s: %d unclosed `{` at end of file", f, depth)
		}
	}
}
