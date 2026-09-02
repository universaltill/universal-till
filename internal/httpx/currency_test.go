package httpx

import "testing"

func TestFormatMoney(t *testing.T) {
	cases := []struct {
		code, locale string
		minor        int64
		want         string
	}{
		// 2-decimal symbol prefix
		{"GBP", "en-US", 123, "£1.23"},
		{"GBP", "en-US", 1234567, "£12,345.67"},
		{"GBP", "en-US", -50, "£-0.50"},
		// rial: no subunit, word AFTER the number (renders left of the
		// digits in RTL text), Persian digits under a fa locale
		{"IRR", "fa", 12345, "۱۲٬۳۴۵ ریال"},
		{"IRR", "en-US", 12345, "12,345 ریال"},
		// toman: also no subunit — 1 toman = 10 rials, never /100
		{"IRT", "fa", 5000, "۵٬۰۰۰ تومان"},
		// 0-decimal prefix currency
		{"JPY", "en-US", 1500, "¥1,500"},
		// unknown code falls back to CODE + 2 decimals
		{"XYZ", "en-US", 199, "XYZ  1.99"},
	}
	for _, c := range cases {
		InitCurrency(c.code)
		if got := FormatMoney(c.minor, c.locale); got != c.want {
			t.Errorf("FormatMoney(%d) %s/%s = %q, want %q", c.minor, c.code, c.locale, got, c.want)
		}
	}
	InitCurrency("GBP")
}

// ut-docs#1274: FormatMajorPlain is the currency-decimals-aware replacement
// for a hardcoded `%d.%02d` against `/100` (shifts_page.go's
// CarryForwardDisplay before this fix) — plain digits only, no symbol, no
// thousands grouping, no locale digit substitution, since it feeds an
// editable decimal-mode input's value attribute that window.utCurrency
// .toMinor() parses back, not a read-only display.
func TestFormatMajorPlain(t *testing.T) {
	cases := []struct {
		minor    int64
		decimals int
		want     string
	}{
		{123, 2, "1.23"},
		{5, 2, "0.05"},
		{-50, 2, "-0.50"},
		// the single most common real input -- every fresh shift-open on a
		// GBP shop with no carry-forward renders exactly this.
		{0, 2, "0.00"},
		// 0-decimal: minor units ARE major units, never divided by 100.
		{500, 0, "500"},
		{-500, 0, "-500"},
		{0, 0, "0"},
		// no thousands grouping, unlike FormatMoney -- this feeds a
		// pattern="[0-9]+(\.[0-9]{1,2})?"-validated input, which a comma
		// would fail.
		{1234567, 2, "12345.67"},
		// generic over decimals, not hardcoded to {0,2} -- the registry only
		// holds those two today, but a future 3-decimal currency
		// (KWD/BHD/OMR) must round-trip through this correctly too.
		{1234, 3, "1.234"},
	}
	for _, c := range cases {
		if got := FormatMajorPlain(c.minor, c.decimals); got != c.want {
			t.Errorf("FormatMajorPlain(%d, %d) = %q, want %q", c.minor, c.decimals, got, c.want)
		}
	}
}

// ut-docs#1400: MinorFromMajor is FormatMajorPlain's write-side inverse,
// used by promotions_page.go/settings_page.go's form handlers instead of a
// hardcoded `* 100` that silently multiplied 0-decimal-currency amounts
// 100x too large.
func TestMinorFromMajor(t *testing.T) {
	cases := []struct {
		major    float64
		decimals int
		want     int64
	}{
		{1.23, 2, 123},
		{0.05, 2, 5},
		{-0.50, 2, -50},
		{0, 2, 0},
		// 0-decimal: major and minor are the same number, never *100.
		{500, 0, 500},
		{-500, 0, -500},
		{0, 0, 0},
		{1234, 3, 1234000},
		// float imprecision (0.1 + 0.2 style) rounds to the nearest minor
		// unit rather than truncating.
		{19.99, 2, 1999},
	}
	for _, c := range cases {
		if got := MinorFromMajor(c.major, c.decimals); got != c.want {
			t.Errorf("MinorFromMajor(%v, %d) = %d, want %d", c.major, c.decimals, got, c.want)
		}
	}
}

// ut-docs#1274: MoneyPattern/MoneyPlaceholder replace 7 duplicated
// {{ if eq currency.Decimals 0 }}…{{ else }}…{1,2}…{{ end }} ternaries
// across shifts.html/reports_tab_tips.html -- generic over decimals (not
// hardcoded to {1,2}), so a future 3-decimal currency (KWD/BHD/OMR) doesn't
// need every call site updated in lockstep to avoid rejecting valid input.
func TestMoneyPattern(t *testing.T) {
	cases := []struct {
		decimals int
		want     string
	}{
		{0, `[0-9]+`},
		{2, `[0-9]+(\.[0-9]{1,2})?`},
		{3, `[0-9]+(\.[0-9]{1,3})?`},
	}
	for _, c := range cases {
		if got := MoneyPattern(c.decimals); got != c.want {
			t.Errorf("MoneyPattern(%d) = %q, want %q", c.decimals, got, c.want)
		}
	}
}

func TestMoneyPlaceholder(t *testing.T) {
	cases := []struct {
		decimals int
		example  int64
		want     string
	}{
		{0, 0, "0"},
		{2, 0, "0.00"},
		{0, 50, "50"},
		{2, 50, "50.00"},
		{3, 50, "50.000"},
		// negative example (adjustment/payout field's placeholder).
		{2, -50, "-50.00"},
		{0, -50, "-50"},
	}
	for _, c := range cases {
		if got := MoneyPlaceholder(c.decimals, c.example); got != c.want {
			t.Errorf("MoneyPlaceholder(%d, %d) = %q, want %q", c.decimals, c.example, got, c.want)
		}
	}
}

// ut-docs#1274 review finding: MoneyPattern/MoneyPlaceholder returning a
// plain string, substituted inside a hand-written `pattern="{{ ... }}"`,
// gets its own '+' HTML-entity-escaped to "&#43;" by html/template's
// contextual auto-escaper -- harmless to a real browser (attribute values
// are entity-decoded before use) but it breaks a literal-`+` grep/test and
// is surprising. MoneyPatternAttr/MoneyPlaceholderAttr produce the WHOLE
// attribute as template.HTMLAttr instead, which bypasses that escaping --
// confirmed via the rendered-page tests in shifts_page_test.go /
// reports_page_test.go (which assert on a literal `pattern="[0-9]+"` in
// the actual HTTP response body); these two just pin the attribute text
// itself, including the signed/negative variants.
func TestMoneyPatternAttr(t *testing.T) {
	cases := []struct {
		decimals int
		signed   bool
		want     string
	}{
		{0, false, `pattern="[0-9]+"`},
		{2, false, `pattern="[0-9]+(\.[0-9]{1,2})?"`},
		{0, true, `pattern="-?[0-9]+"`},
		{2, true, `pattern="-?[0-9]+(\.[0-9]{1,2})?"`},
	}
	for _, c := range cases {
		if got := string(MoneyPatternAttr(c.decimals, c.signed)); got != c.want {
			t.Errorf("MoneyPatternAttr(%d, %v) = %q, want %q", c.decimals, c.signed, got, c.want)
		}
	}
}

func TestMoneyPlaceholderAttr(t *testing.T) {
	cases := []struct {
		decimals int
		example  int64
		want     string
	}{
		{0, 0, `placeholder="0"`},
		{2, 0, `placeholder="0.00"`},
		{2, -50, `placeholder="-50.00"`},
	}
	for _, c := range cases {
		if got := string(MoneyPlaceholderAttr(c.decimals, c.example)); got != c.want {
			t.Errorf("MoneyPlaceholderAttr(%d, %d) = %q, want %q", c.decimals, c.example, got, c.want)
		}
	}
}

func TestLocalizeDigits(t *testing.T) {
	if got := LocalizeDigits("12,345.60", "fa-IR"); got != "۱۲٬۳۴۵٫۶۰" {
		t.Errorf("fa digits = %q", got)
	}
	if got := LocalizeDigits("12,345.60", "ar"); got != "١٢٬٣٤٥٫٦٠" {
		t.Errorf("ar digits = %q", got)
	}
	if got := LocalizeDigits("12,345.60", "en-US"); got != "12,345.60" {
		t.Errorf("latin passthrough = %q", got)
	}
}
