package httpx

import (
	"strings"
	"time"
)

// dateLayout returns the Go reference-time layout for locale's date-
// ordering convention (ut-docs#1130). Table-driven, the same per-locale-
// family pattern as localeDigits/numberSeparators — keyed on the full tag
// where region changes the order (en-US is month-first; every other en
// variant, GB included, is day-first) and on base language otherwise.
// Covers every one of this product's unconditionally-preset country
// defaults (internal/data.BuiltinCountryDefaults), same coverage
// reasoning as numberSeparators' own doc comment. Defaults to
// day/month/year (slash-separated) for anything not listed — the
// convention most of this product's shipped locales (fa/ar, and GB/FR/
// ES/IT English/French/Spanish/Italian) already use, so only en-US
// (month-first) and de/tr (dot-separated) / nl (hyphen-separated) need
// an entry.
func dateLayout(locale string) string {
	lang := strings.ToLower(locale)
	if lang == "en-us" {
		return "01/02/2006"
	}
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	switch lang {
	case "de", "tr":
		return "02.01.2006"
	case "nl":
		return "02-01-2006"
	default:
		return "02/01/2006"
	}
}

// FormatDate renders t in locale's date-ordering convention, with digit
// shape following locale the same way FormatMoney does (LocalizeDigits) —
// "2026-09-06" renders "06.09.2026" for de-DE, "۰۶/۰۹/۲۰۲۶" for fa.
func FormatDate(t time.Time, locale string) string {
	return LocalizeDigits(t.Format(dateLayout(locale)), locale)
}

// FormatDateLatin is FormatDate without digit-shape substitution — for
// contexts (ESC/POS receipt printing) that need Latin digits only in text
// mode, same reasoning as FormatMoneyLatin.
func FormatDateLatin(t time.Time, locale string) string {
	return t.Format(dateLayout(locale))
}
