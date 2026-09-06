package httpx

import (
	"testing"
	"time"
)

// ut-docs#1130: date ordering follows locale — DE/TR are day.month.year,
// NL is day-month-year (hyphens), US is month/day/year, everything else
// (GB/FR/ES/IT included) defaults to day/month/year with slashes. Covers
// every one of this product's unconditionally-preset country defaults —
// review finding: a table with only DE/en-US listed left TR (a BUNDLED
// UI locale, not just a plugin-supplied one) and NL wrong. Digit shape
// follows locale the same way FormatMoney's does, and is a genuine
// regression check for fa: the existing digit-substitution mechanism
// must still work, unchanged, composed with the new date-order table.
func TestFormatDate(t *testing.T) {
	d := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	cases := []struct{ locale, want string }{
		{"de-DE", "06.09.2026"},
		{"de", "06.09.2026"},
		{"tr", "06.09.2026"}, // bundled UI locale — must NOT keep the default
		{"nl-NL", "06-09-2026"},
		{"en-GB", "06/09/2026"},
		{"en-US", "09/06/2026"},
		{"en", "06/09/2026"}, // bare "en" defaults day-first, like GB
		{"fr-FR", "06/09/2026"},
		{"es-ES", "06/09/2026"},
	}
	for _, c := range cases {
		if got := FormatDate(d, c.locale); got != c.want {
			t.Errorf("FormatDate(%s) = %q, want %q", c.locale, got, c.want)
		}
	}
	if got := FormatDate(d, "fa"); got != "۰۶/۰۹/۲۰۲۶" {
		t.Errorf("FormatDate fa = %q, want Persian digits", got)
	}
}

func TestFormatDateLatin(t *testing.T) {
	d := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	if got := FormatDateLatin(d, "de-DE"); got != "06.09.2026" {
		t.Errorf("FormatDateLatin de-DE = %q", got)
	}
	// fa would digit-substitute under FormatDate; Latin variant must not —
	// same ESC/POS text-mode constraint as FormatMoneyLatin/FormatQtyLatin.
	if got := FormatDateLatin(d, "fa"); got != "06/09/2026" {
		t.Errorf("FormatDateLatin fa = %q, want Latin digits", got)
	}
}
