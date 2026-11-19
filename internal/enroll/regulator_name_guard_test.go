package enroll

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// ut-docs#6278: since 2026-09-30 the UK data protection regulator is the
// Information Commission (Data (Use and Access) Act 2025 ss.117-119,
// SI 2026/1015); the Information Commissioner's Office is its abolished
// predecessor. No English string may name the old office as the current
// regulator. A "formerly ..." bridge sentence is the one allowed use.
// bridgePhrase is the one allowed use of the old name (both apostrophes,
// any case). Only this exact phrase is removed; a "formerly" anywhere else
// in a string exempts nothing.
var bridgePhrase = regexp.MustCompile(`(?i)formerly the Information Commissioner['’]s Office`)

// namesOldUKRegulator reports the old-name fragments still present in text
// after the bridge phrase is removed (case-insensitive).
func namesOldUKRegulator(text string) []string {
	rest := strings.ToLower(bridgePhrase.ReplaceAllString(text, ""))
	var hits []string
	for _, old := range []string{"commissioner's office", "commissioner’s office", "information commissioner"} {
		if strings.Contains(rest, old) {
			hits = append(hits, old)
		}
	}
	return hits
}

func TestNamesOldUKRegulator(t *testing.T) {
	cases := []struct {
		name, text string
		bad        bool
	}{
		{"new name", "in the UK that is the Information Commission (ico.org.uk).", false},
		{"bridge straight", "the Information Commission (formerly the Information Commissioner's Office)", false},
		{"bridge curly, upper case", "the Information Commission (Formerly the Information Commissioner’s Office)", false},
		{"old name alone", "Complain to the Information Commissioner's Office.", true},
		{"old name curly", "Complain to the Information Commissioner’s Office.", true},
		{"lower case", "complain to the information commissioner's office", true},
		{"bare Commissioner's Office", "Complain to the Commissioner's Office.", true},
		{"formerly elsewhere (F1)", "This screen was formerly called Data. Complain to the Information Commissioner's Office.", true},
		{"bridge plus a second bare use", "(formerly the Information Commissioner's Office) and the Information Commissioner's Office", true},
	}
	for _, c := range cases {
		if got := len(namesOldUKRegulator(c.text)) > 0; got != c.bad {
			t.Errorf("%s: flagged=%v, want %v (%q)", c.name, got, c.bad, c.text)
		}
	}
}

func TestEnglishStringsDoNotNameOldUKRegulator(t *testing.T) {
	raw, err := os.ReadFile("../../web/locales/en.json")
	if err != nil {
		t.Fatal(err)
	}
	var en map[string]string
	if err := json.Unmarshal(raw, &en); err != nil {
		t.Fatal(err)
	}
	for key, text := range en {
		for _, old := range namesOldUKRegulator(text) {
			t.Errorf("%s names the old UK regulator %q; use \"Information Commission\" (ut-docs#6278)", key, old)
		}
	}
}
