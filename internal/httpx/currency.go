package httpx

import (
	"fmt"
	"html/template"
	"strings"
)

// CurrencyInfo describes how a currency is written. Display is a symbol (£)
// or a word (ریال); Suffix places it after the number in logical order —
// which is visually to the LEFT of the digits in RTL text, matching how
// rial/toman prices are written. Decimals is the minor-unit exponent:
// GBP = 2 (100 pence per pound), IRR/IRT = 0 (rial and toman have no
// working subunit — 1 toman = 10 rials, and neither divides by 100).
type CurrencyInfo struct {
	Code     string
	Name     string // picker label
	Display  string // symbol or word
	Suffix   bool   // display goes after the number (logical order)
	Decimals int    // minor units per major = 10^Decimals
}

// The registry the Settings picker offers. Minor units in the database are
// the smallest unit of the SELECTED currency: pence for GBP, whole rials for
// IRR, whole tomans for IRT — switching currency does not convert amounts.
var currencyRegistry = []CurrencyInfo{
	{Code: "GBP", Name: "British Pound (£)", Display: "£", Decimals: 2},
	{Code: "USD", Name: "US Dollar ($)", Display: "$", Decimals: 2},
	{Code: "EUR", Name: "Euro (€)", Display: "€", Decimals: 2},
	{Code: "IRR", Name: "Iranian Rial (ریال)", Display: "ریال", Suffix: true, Decimals: 0},
	{Code: "IRT", Name: "Iranian Toman (تومان)", Display: "تومان", Suffix: true, Decimals: 0},
	{Code: "TRY", Name: "Turkish Lira (₺)", Display: "₺", Decimals: 2},
	{Code: "AED", Name: "UAE Dirham (د.إ)", Display: "د.إ", Suffix: true, Decimals: 2},
	{Code: "SAR", Name: "Saudi Riyal (ر.س)", Display: "ر.س", Suffix: true, Decimals: 2},
	{Code: "IQD", Name: "Iraqi Dinar (د.ع)", Display: "د.ع", Suffix: true, Decimals: 0},
	{Code: "AFN", Name: "Afghan Afghani (؋)", Display: "؋", Suffix: true, Decimals: 0},
	{Code: "INR", Name: "Indian Rupee (₹)", Display: "₹", Decimals: 2},
	{Code: "PKR", Name: "Pakistani Rupee (₨)", Display: "₨", Decimals: 2},
	{Code: "JPY", Name: "Japanese Yen (¥)", Display: "¥", Decimals: 0},
}

// Currencies returns the registry for the Settings picker.
func Currencies() []CurrencyInfo { return currencyRegistry }

// CurrencyByCode resolves a code; unknown codes fall back to "CODE 1.23".
// This fallback makes CurrencyByCode unsuitable as a validity check on its
// own (ut-docs#970 review, finding F1) — CurrencyByCode(v).Code == v is
// ALWAYS true for an already-uppercased/trimmed v, known or not, so that
// idiom silently accepts anything. Callers that need to reject an unknown
// code must use IsKnownCurrency instead.
func CurrencyByCode(code string) CurrencyInfo {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, c := range currencyRegistry {
		if c.Code == code {
			return c
		}
	}
	return CurrencyInfo{Code: code, Name: code, Display: code + " ", Decimals: 2}
}

// IsKnownCurrency reports whether code is a real entry in the registry (the
// only set the Settings/setup currency pickers actually offer) — unlike
// CurrencyByCode, this genuinely rejects an unrecognised code instead of
// silently fabricating a plausible-looking CurrencyInfo for it.
func IsKnownCurrency(code string) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, c := range currencyRegistry {
		if c.Code == code {
			return true
		}
	}
	return false
}

// ActiveCurrency returns the configured currency's formatting info.
func ActiveCurrency() CurrencyInfo {
	code := "GBP"
	if v := currencyCode.Load(); v != nil {
		if s, ok := v.(string); ok && s != "" {
			code = s
		}
	}
	return CurrencyByCode(code)
}

// Digit sets for locales that write numbers in their own numerals.
var (
	digitsFa = [10]rune{'۰', '۱', '۲', '۳', '۴', '۵', '۶', '۷', '۸', '۹'} // Extended Arabic-Indic
	digitsAr = [10]rune{'٠', '١', '٢', '٣', '٤', '٥', '٦', '٧', '٨', '٩'} // Arabic-Indic
)

// localeDigits returns the digit set for a locale, or nil for Latin digits.
func localeDigits(locale string) *[10]rune {
	lang := strings.ToLower(locale)
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	switch lang {
	case "fa", "ur", "ps":
		return &digitsFa
	case "ar":
		return &digitsAr
	}
	return nil
}

// LocalizeDigits rewrites ASCII digits (and separators) into the locale's
// numerals — "12,345.60" → "۱۲٬۳۴۵٫۶۰" for fa. Latin locales pass through.
func LocalizeDigits(s, locale string) string {
	set := localeDigits(locale)
	if set == nil {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(set[r-'0'])
		case r == ',':
			b.WriteRune('٬') // Arabic thousands separator
		case r == '.':
			b.WriteRune('٫') // Arabic decimal separator
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// groupThousands inserts commas into an unsigned integer string.
func groupThousands(s string) string {
	n := len(s)
	if n <= 3 {
		return s
	}
	var b strings.Builder
	head := n % 3
	if head > 0 {
		b.WriteString(s[:head])
	}
	for i := head; i < n; i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// FormatMoney renders minor units in the active currency for a locale:
// decimals, symbol-vs-word placement, thousands grouping, and localized
// digits all follow the currency + locale ("£1.23", "12,345 ریال" → rendered
// as "۱۲٬۳۴۵ ریال" under a fa locale).
func FormatMoney(minor int64, locale string) string {
	c := ActiveCurrency()
	neg := minor < 0
	if neg {
		minor = -minor
	}
	var num string
	if c.Decimals <= 0 {
		num = groupThousands(fmt.Sprintf("%d", minor))
	} else {
		pow := int64(1)
		for i := 0; i < c.Decimals; i++ {
			pow *= 10
		}
		num = fmt.Sprintf("%s.%0*d", groupThousands(fmt.Sprintf("%d", minor/pow)), c.Decimals, minor%pow)
	}
	if neg {
		num = "-" + num
	}
	num = LocalizeDigits(num, locale)
	if c.Suffix {
		return num + " " + c.Display
	}
	// single-rune symbols hug the number; words/codes get a space
	if len([]rune(strings.TrimSpace(c.Display))) > 1 {
		return c.Display + " " + num
	}
	return c.Display + num
}

// FormatMajorPlain renders minor units as a plain decimal major-unit string
// — no symbol, no thousands grouping, no locale digit substitution — for
// prefilling an editable decimal-mode input that window.utCurrency.toMinor()
// (or an equivalent Go-side parse) will convert back to minor units.
// decimals-aware unlike a hardcoded `/100`: a 0-decimal currency's minor
// units ARE its major units (500 IRT stays "500", never "5.00").
// ut-docs#1274: CarryForwardDisplay used to hardcode `%d.%02d` against
// `/100`, silently wrong on any 0-decimal shop (IRR/IRT/IQD/AFN/JPY).
func FormatMajorPlain(minor int64, decimals int) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	var s string
	if decimals <= 0 {
		s = fmt.Sprintf("%d", minor)
	} else {
		pow := int64(1)
		for i := 0; i < decimals; i++ {
			pow *= 10
		}
		s = fmt.Sprintf("%d.%0*d", minor/pow, decimals, minor%pow)
	}
	if neg {
		s = "-" + s
	}
	return s
}

// MoneyPattern renders the value a decimal-mode money input's `pattern`
// attribute should hold at the given currency's decimals: integer-only for
// a 0-decimal currency, otherwise up to `decimals` fractional digits.
// Generic over decimals rather than hardcoded to 2 -- the registry only
// holds 0- and 2-decimal currencies today, but a hardcoded `{1,2}` would
// silently reject valid input the day a 3-decimal currency (KWD/BHD/OMR)
// is added, the same class of bug this whole card (ut-docs#1274) fixes for
// `/100`. Plain string, for use from Go and from tests -- template callers
// use MoneyPatternAttr instead (see its doc comment for why).
func MoneyPattern(decimals int) string {
	if decimals <= 0 {
		return `[0-9]+`
	}
	return fmt.Sprintf(`[0-9]+(\.[0-9]{1,%d})?`, decimals)
}

// MoneyPlaceholder renders an example major-unit amount at the given
// currency's decimals: MoneyPlaceholder(2, 50) -> "50.00",
// MoneyPlaceholder(0, 50) -> "50", MoneyPlaceholder(3, 50) -> "50.000".
// example may be negative (an adjustment/payout field's example, e.g.
// -50 -> "-50.00"). Plain string, for use from Go and from tests --
// template callers use MoneyPlaceholderAttr instead.
func MoneyPlaceholder(decimals int, example int64) string {
	if decimals <= 0 {
		return fmt.Sprintf("%d", example)
	}
	return fmt.Sprintf("%d.%s", example, strings.Repeat("0", decimals))
}

// MoneyPatternAttr renders the WHOLE `pattern="…"` HTML attribute (not
// just its value) for a decimal-mode money input -- see MoneyPattern for
// the regex shape. signed prepends `-?` for a field that allows a leading
// minus (e.g. a payout/adjustment).
//
// Returns template.HTMLAttr, not string: the regex contains a literal '+',
// and html/template's contextual auto-escaper HTML-entity-encodes it to
// "&#43;" whenever a plain string/template.HTML value is substituted
// INSIDE an already hand-quoted `pattern="{{ ... }}"` -- harmless to a
// real browser (attribute values are entity-decoded before use, so the
// pattern still validates correctly) but it breaks a literal-`+` substring
// check and departs from how this same pattern reads everywhere else it's
// still hand-typed directly into markup (e.g. index.html's #pfand-amount).
// template.HTMLAttr only suppresses that escaping when the ACTION PRODUCES
// THE WHOLE ATTRIBUTE (`{{ moneypattern … }}`, no surrounding
// `pattern="…"` in the template source) -- confirmed empirically, a
// template.HTML value substituted inside hand-written quotes still gets
// escaped the same way a plain string does.
func MoneyPatternAttr(decimals int, signed bool) template.HTMLAttr {
	p := MoneyPattern(decimals)
	if signed {
		p = "-?" + p
	}
	return template.HTMLAttr(`pattern="` + p + `"`)
}

// MoneyPlaceholderAttr renders the whole `placeholder="…"` HTML attribute
// for an example major-unit amount (may be negative) -- see
// MoneyPlaceholder. Same template.HTMLAttr reasoning as MoneyPatternAttr
// (the placeholder is plain digits/'.'/'-' so escaping is harmless here
// too, but the WHOLE-attribute convention is kept consistent between the
// two so a template author reaches for the right one without re-deriving
// which is safe).
func MoneyPlaceholderAttr(decimals int, example int64) template.HTMLAttr {
	return template.HTMLAttr(`placeholder="` + MoneyPlaceholder(decimals, example) + `"`)
}
