package httpx

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPercentHelpTopicsStateTwoDecimalLimit keeps the manual honest about
// PercentPatternLocal (ut-docs#4446): every translation of the two topics
// that describe entering a tax rate gives the refused 3-decimal example, and
// the examples it gives really are accepted/refused by the field's pattern.
func TestPercentHelpTopicsStateTwoDecimalLimit(t *testing.T) {
	re := regexp.MustCompile(`^(?:` + PercentPatternLocal + `)$`)
	topics := map[string]struct{ ok, refused string }{
		"tax-codes.md":        {"7.25", "8.875"},
		"country-settings.md": {"8.25", "8.875"},
	}
	for file, ex := range topics {
		if !re.MatchString(ex.ok) {
			t.Errorf("%s: example %q should match PercentPatternLocal", file, ex.ok)
		}
		if re.MatchString(ex.refused) {
			t.Errorf("%s: example %q should not match PercentPatternLocal", file, ex.refused)
		}
	}
	locales, err := os.ReadDir(filepath.Join("..", "..", "web", "help"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, l := range locales {
		if !l.IsDir() || l.Name() == "img" {
			continue
		}
		for file, ex := range topics {
			b, err := os.ReadFile(filepath.Join("..", "..", "web", "help", l.Name(), file))
			if err != nil {
				t.Fatal(err)
			}
			checked++
			for _, want := range []string{"`" + ex.ok + "`", "`" + ex.refused + "`"} {
				if !strings.Contains(string(b), want) {
					t.Errorf("web/help/%s/%s: missing the two-decimal example %s", l.Name(), file, want)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no help locales found")
	}
}
