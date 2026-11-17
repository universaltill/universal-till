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
	// same ESC/POS text-mode constraint as FormatMoneyPrint/FormatQtyLatin.
	if got := FormatDateLatin(d, "fa"); got != "06/09/2026" {
		t.Errorf("FormatDateLatin fa = %q, want Latin digits", got)
	}
}

// ut-docs#1632: FormatDateTime is FormatDate plus a 24-hour clock, for a
// per-event timestamp (journal/audit/my-reports rows) where the time of
// day matters and multiple rows can share a calendar date — as opposed to
// FormatDate's date-only rendering (an invoice's issue date). Same
// date-order table and digit-shape substitution as FormatDate, applied to
// the whole "date time" string so the clock digits localize too.
func TestFormatDateTime(t *testing.T) {
	d := time.Date(2026, 9, 6, 14, 5, 0, 0, time.UTC)
	cases := []struct{ locale, want string }{
		{"de-DE", "06.09.2026 14:05"},
		{"en-GB", "06/09/2026 14:05"},
		{"en-US", "09/06/2026 14:05"},
	}
	for _, c := range cases {
		if got := FormatDateTime(d, c.locale); got != c.want {
			t.Errorf("FormatDateTime(%s) = %q, want %q", c.locale, got, c.want)
		}
	}
	// Digit shape follows locale for the whole string, clock included —
	// same as FormatDate's own fa case.
	if got := FormatDateTime(d, "fa"); got != "۰۶/۰۹/۲۰۲۶ ۱۴:۰۵" {
		t.Errorf("FormatDateTime fa = %q, want Persian digits throughout", got)
	}
}

func TestFormatDateTimeLatin(t *testing.T) {
	d := time.Date(2026, 9, 6, 14, 5, 0, 0, time.UTC)
	if got := FormatDateTimeLatin(d, "de-DE"); got != "06.09.2026 14:05" {
		t.Errorf("FormatDateTimeLatin de-DE = %q", got)
	}
	if got := FormatDateTimeLatin(d, "fa"); got != "06/09/2026 14:05" {
		t.Errorf("FormatDateTimeLatin fa = %q, want Latin digits", got)
	}
}

// ut-docs#2742: the status-bar chip says "since 14:05" for an outage that
// started today, and the full date only when it began on another day.
func TestFormatShortDateTime(t *testing.T) {
	now := time.Date(2026, 9, 25, 16, 0, 0, 0, time.UTC)
	if got := FormatShortDateTime(time.Date(2026, 9, 25, 14, 5, 0, 0, time.UTC), now, "en-GB"); got != "14:05" {
		t.Errorf("today = %q, want 14:05", got)
	}
	if got := FormatShortDateTime(time.Date(2026, 9, 24, 23, 55, 0, 0, time.UTC), now, "de-DE"); got != "24.09.2026 23:55" {
		t.Errorf("yesterday = %q, want the full date", got)
	}
	if got := FormatShortDateTime(time.Date(2026, 9, 25, 14, 5, 0, 0, time.UTC), now, "fa"); got != LocalizeDigits("14:05", "fa") {
		t.Errorf("fa today = %q, want localized digits", got)
	}
}

// ut-docs#5748: date order follows the shop's region for a bare same-language pick.
func dateFixture5748() time.Time { return time.Date(2025, 11, 6, 21, 33, 0, 0, time.Local) }

func TestDateLocale_KeepsShopRegionForSameLanguage(t *testing.T) {
	defer SnapshotStateForTests()()
	cases := []struct{ shop, display, want string }{
		{"en-US", "en", "en-US"},
		{"en-GB", "en", "en-GB"},
		{"en-US", "EN", "en-US"},
		{"en-US", "en-GB", "en-GB"},
		{"en-US", "tr", "tr"},
		{"de-DE", "de", "de-DE"},
		{"de-DE", "en", "en"},
		{"en-US", "", "en-US"},
	}
	for _, c := range cases {
		SetDefaultLocale(c.shop)
		if got := DateLocale(c.display); got != c.want {
			t.Errorf("shop %q display %q: got %q want %q", c.shop, c.display, got, c.want)
		}
	}
}

func TestFormatDate_BareEnFollowsShopRegion_5748(t *testing.T) {
	defer SnapshotStateForTests()()
	d := dateFixture5748()
	for shop, want := range map[string]string{"en-US": "11/06/2025", "en-GB": "06/11/2025"} {
		SetDefaultLocale(shop)
		for name, got := range map[string]string{
			"FormatDate": FormatDate(d, "en"), "FormatDateLatin": FormatDateLatin(d, "en"),
		} {
			if got != want {
				t.Errorf("shop %s %s(en) = %q want %q", shop, name, got, want)
			}
		}
		for name, got := range map[string]string{
			"FormatDateTime": FormatDateTime(d, "en"), "FormatDateTimeLatin": FormatDateTimeLatin(d, "en"),
			"paper": FormatDateTimeLatin(d, DefaultLocale()),
		} {
			if got != want+" 21:33" {
				t.Errorf("shop %s %s = %q want %q", shop, name, got, want+" 21:33")
			}
		}
	}
}

func TestFormatDate_OtherLocalesUnchanged_5748(t *testing.T) {
	defer SnapshotStateForTests()()
	SetDefaultLocale("en-US")
	d := dateFixture5748()
	for loc, want := range map[string]string{
		"de-DE": "06.11.2025", "de": "06.11.2025", "nl-NL": "06-11-2025", "tr": "06.11.2025",
		"fr-FR": "06/11/2025", "en-GB": "06/11/2025", "en_US": "11/06/2025", "EN-us": "11/06/2025",
	} {
		if got := FormatDateLatin(d, loc); got != want {
			t.Errorf("FormatDateLatin(%q) = %q want %q", loc, got, want)
		}
	}
	if got := FormatDate(d, "fa"); got != "۰۶/۱۱/۲۰۲۵" {
		t.Errorf("FormatDate(fa) = %q", got)
	}
	if got := FormatDateLatin(d, "fa"); got != "06/11/2025" {
		t.Errorf("FormatDateLatin(fa) = %q", got)
	}
}
