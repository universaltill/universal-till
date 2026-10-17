package httpx

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPercentHelpTopicsStateTwoDecimalLimit keeps the manual honest about
// PercentPatternLocal (ut-docs#4446): every translation of each topic that
// describes a percent field (tax rate, promotion percent-off, payment-fee
// percent, plugin takeaway rate — ut-docs#4498) gives the accepted and the
// refused 3-decimal example, and those examples really are accepted/refused
// by the field's pattern.
func TestPercentHelpTopicsStateTwoDecimalLimit(t *testing.T) {
	re := regexp.MustCompile(`^(?:` + PercentPatternLocal + `)$`)
	topics := map[string]struct{ ok, refused string }{
		"tax-codes.md":        {"7.25", "8.875"},
		"country-settings.md": {"8.25", "8.875"},
		"promotions.md":       {"12.25", "12.125"},
		"payments.md":         {"2.75", "2.875"},
		"plugins.md":          {"7.25", "8.875"},
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
