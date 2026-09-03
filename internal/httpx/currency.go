package httpx

import (
	"fmt"
	"html/template"
	"math"
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
	// Denominations lists the currency's real circulating physical
	// banknotes and coins, in minor units, strictly descending (largest
	// first — the order the shift-close cash-count grid renders in,
	// ut-docs#1291). Unlike Decimals/Display this has no algorithmic
	// derivation — different currencies have entirely different physical
	// denominations, not just a different symbol or decimal count — so
	// every registry entry lists its own.
	Denominations []int64
}

// The registry the Settings picker offers. Minor units in the database are
// the smallest unit of the SELECTED currency: pence for GBP, whole rials for
// IRR, whole tomans for IRT — switching currency does not convert amounts.
var currencyRegistry = []CurrencyInfo{
	{Code: "GBP", Name: "British Pound (£)", Display: "£", Decimals: 2,
		// Notes: £50,20,10,5. Coins: £2,1, 50p,20p,10p,5p,2p,1p.
		Denominations: []int64{5000, 2000, 1000, 500, 200, 100, 50, 20, 10, 5, 2, 1}},
	{Code: "USD", Name: "US Dollar ($)", Display: "$", Decimals: 2,
		// Notes: $100,50,20,10,5,1. Coins: 25c,10c,5c,1c.
		Denominations: []int64{10000, 5000, 2000, 1000, 500, 100, 25, 10, 5, 1}},
	{Code: "EUR", Name: "Euro (€)", Display: "€", Decimals: 2,
		// Notes: €200,100,50,20,10,5. Coins: €2,1, 50c,20c,10c,5c,2c,1c.
		// €500 excluded — issuance discontinued in 2019; still legal
		// tender but actively withdrawn, so not a till-drawer row. €200
		// is NOT in that category (current Europa-series note, still
		// issued), so it is listed — review of ut-docs#1291.
		Denominations: []int64{20000, 10000, 5000, 2000, 1000, 500, 200, 100, 50, 20, 10, 5, 2, 1}},
	{Code: "IRR", Name: "Iranian Rial (ریال)", Display: "ریال", Suffix: true, Decimals: 0,
		// Rial coins are obsolete; only banknotes circulate.
		Denominations: []int64{1000000, 500000, 100000, 50000, 20000, 10000, 5000, 2000, 1000}},
	{Code: "IRT", Name: "Iranian Toman (تومان)", Display: "تومان", Suffix: true, Decimals: 0,
		// Same physical notes as IRR, quoted at 1/10th (1 toman = 10 rials).
		Denominations: []int64{100000, 50000, 10000, 5000, 2000, 1000, 500, 200, 100}},
	{Code: "TRY", Name: "Turkish Lira (₺)", Display: "₺", Decimals: 2,
		// Notes: ₺200,100,50,20,10,5. Coins: ₺1, 50kr,25kr,10kr,5kr.
		Denominations: []int64{20000, 10000, 5000, 2000, 1000, 500, 100, 50, 25, 10, 5}},
	{Code: "AED", Name: "UAE Dirham (د.إ)", Display: "د.إ", Suffix: true, Decimals: 2,
		// Notes: 1000,500,200,100,50,20,10,5. Coins: 1dh, 50,25 fils.
		Denominations: []int64{100000, 50000, 20000, 10000, 5000, 2000, 1000, 500, 100, 50, 25}},
	{Code: "SAR", Name: "Saudi Riyal (ر.س)", Display: "ر.س", Suffix: true, Decimals: 2,
		// Notes: 500,200,100,50,10,5,1. Coins: 2 and 1 riyal, then
		// 50,25,10,5,1 halala. The 2-riyal coin (200 halalas, sixth
		// series 2016) was missing from the first draft — review of
		// ut-docs#1291.
		Denominations: []int64{50000, 20000, 10000, 5000, 1000, 500, 200, 100, 50, 25, 10, 5, 1}},
	{Code: "IQD", Name: "Iraqi Dinar (د.ع)", Display: "د.ع", Suffix: true, Decimals: 0,
		// Fils coins are obsolete; only banknotes circulate.
		Denominations: []int64{50000, 25000, 10000, 5000, 1000, 500, 250}},
	{Code: "AFN", Name: "Afghan Afghani (؋)", Display: "؋", Suffix: true, Decimals: 0,
		// Notes: 1000..10. Coins: 5,2,1 (rarely seen but still legal tender).
		Denominations: []int64{1000, 500, 100, 50, 20, 10, 5, 2, 1}},
	{Code: "INR", Name: "Indian Rupee (₹)", Display: "₹", Decimals: 2,
		// Notes: 500,200,100,50,20,10 (2000 excluded — withdrawn from
		// circulation). Coins: 20,10,5,2,1 rupee (paise coins obsolete);
		// the ₹20/₹10 coin values coincide with the ₹20/₹10 note values,
		// so each appears once.
		Denominations: []int64{50000, 20000, 10000, 5000, 2000, 1000, 500, 200, 100}},
	{Code: "PKR", Name: "Pakistani Rupee (₨)", Display: "₨", Decimals: 2,
		// Notes: 5000,1000,500,100,50,20,10. Coins: 10,5,2,1 rupee (paisa
		// coins obsolete); the ₨10 coin value coincides with the ₨10 note.
		Denominations: []int64{500000, 100000, 50000, 10000, 5000, 2000, 1000, 500, 200, 100}},
	{Code: "JPY", Name: "Japanese Yen (¥)", Display: "¥", Decimals: 0,
		// Notes: 10000,5000,1000 (2000 excluded — rare, not commonly
		// stocked in a till). Coins: 500,100,50,10,5,1.
		Denominations: []int64{10000, 5000, 1000, 500, 100, 50, 10, 5, 1}},
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

// MinorFromMajor converts a value entered in major units (e.g. "5.00" under
// a 2-decimal currency, or "500" under a 0-decimal one) into minor units --
// the write-side inverse of FormatMajorPlain, for a Go-side form handler
// parsing a major-unit amount instead of relying on
// window.utCurrency.toMinor() client-side. Rounds to the nearest minor unit
// to absorb float parsing imprecision, same as the hardcoded `* 100`
// literals this replaces already did for 2-decimal currencies.
// ut-docs#1400: promotions_page.go/settings_page.go hardcoded `* 100`
// regardless of the active currency's decimals, storing a 100x-too-large
// value on a 0-decimal shop (IRR/IRT/IQD/AFN/JPY) -- e.g. an operator
// entering "500" for ¥500 got 50000 minor units persisted.
func MinorFromMajor(major float64, decimals int) int64 {
	pow := int64(1)
	for i := 0; i < decimals; i++ {
		pow *= 10
	}
	return int64(math.Round(major * float64(pow)))
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
