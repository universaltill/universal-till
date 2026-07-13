package httpx

import (
	"fmt"
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
func CurrencyByCode(code string) CurrencyInfo {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, c := range currencyRegistry {
		if c.Code == code {
			return c
		}
	}
	return CurrencyInfo{Code: code, Name: code, Display: code + " ", Decimals: 2}
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
