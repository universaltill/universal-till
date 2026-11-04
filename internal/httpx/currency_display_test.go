package httpx

import "testing"

// ut-docs#5410: the shop-wide currency display mode (auto|symbol|code).
func TestFormatMoneyWith_DisplayMatrix(t *testing.T) {
	cases := []struct {
		code, mode, locale string
		minor              int64
		want               string
	}{
		{"AED", "auto", "en", 1250, "AED 12.50"},
		{"AED", "", "en", 1250, "AED 12.50"},
		{"AED", "symbol", "en", 1250, "12.50 د.إ"},
		{"AED", "code", "en", 1250, "AED 12.50"},
		{"AED", "auto", "en", -1250, "-AED 12.50"},
		{"GBP", "auto", "en", 1250, "£12.50"},
		{"GBP", "symbol", "en", 1250, "£12.50"},
		{"GBP", "code", "en", 1250, "GBP 12.50"},
		{"GBP", "code", "en", -1250, "-GBP 12.50"},
		{"EUR", "auto", "en", 1250, "€12.50"},
		{"IRR", "auto", "en", 1250, "IRR 1,250"},
		{"IRR", "symbol", "en", 1250, "1,250 ریال"},
		{"IRT", "auto", "en", 500, "IRT 500"},
		{"SAR", "auto", "en", 100, "SAR 1.00"},
		{"IQD", "auto", "en", 2500, "IQD 2,500"},
		{"TRY", "auto", "en", 1250, "₺12.50"},
		{"INR", "auto", "en", 1250, "₹12.50"},
		{"JPY", "auto", "en", 300, "¥300"},
		{"JPY", "code", "en", 300, "JPY 300"},
		{"TRY", "symbol", "en", 1250, "₺12.50"},
		{"AED", "bogus", "en", 1250, "AED 12.50"}, // unknown mode behaves as auto
		{"GBP", "bogus", "en", 1250, "£12.50"},
	}
	for _, c := range cases {
		got := FormatMoneyWith(c.minor, CurrencyByCode(c.code), c.locale, true, c.mode)
		if got != c.want {
			t.Errorf("%s/%s/%d: got %q want %q", c.code, c.mode, c.minor, got, c.want)
		}
	}
}

func TestFormatMoneyIn_ForeignCodeFollowsMode(t *testing.T) {
	defer SnapshotStateForTests()()
	InitCurrency("GBP")
	InitCurrencyDisplay("auto")
	if got := FormatMoneyIn(1250, "AED", "en"); got != "AED 12.50" {
		t.Errorf("auto AED: %q", got)
	}
	if got := FormatMoneyIn(1250, "USD", "en"); got != "$12.50" {
		t.Errorf("auto USD: %q", got)
	}
	InitCurrencyDisplay("code")
	if got := FormatMoneyIn(1250, "USD", "en"); got != "USD 12.50" {
		t.Errorf("code USD: %q", got)
	}
	// unknown code unchanged in every mode
	for _, m := range []string{"auto", "symbol", "code"} {
		InitCurrencyDisplay(m)
		if got := FormatMoneyIn(12500, "KWD", "en"); got != "KWD 12.500" {
			t.Errorf("%s KWD: %q", m, got)
		}
	}
}

func TestActiveCurrency_ResolvedByMode(t *testing.T) {
	defer SnapshotStateForTests()()
	InitCurrency("AED")
	InitCurrencyDisplay("auto")
	c := ActiveCurrency()
	if c.Display != "AED" || c.Suffix || c.Code != "AED" || c.Decimals != 2 {
		t.Errorf("auto AED: %+v", c)
	}
	InitCurrencyDisplay("symbol")
	c = ActiveCurrency()
	if c.Display != "د.إ" || !c.Suffix {
		t.Errorf("symbol AED: %+v", c)
	}
	if got := FormatMoney(1250, "en"); got != "12.50 د.إ" {
		t.Errorf("FormatMoney symbol: %q", got)
	}
	InitCurrencyDisplay("auto")
	if got := FormatMoney(1250, "en"); got != "AED 12.50" {
		t.Errorf("FormatMoney auto: %q", got)
	}
	if got := FormatMoneyPrint(1250, "en", "utf8"); got != "AED 12.50" {
		t.Errorf("FormatMoneyPrint auto: %q", got)
	}
}

func TestFormatMoneyDisplay_RTLCodePrefixNegativeIsolated(t *testing.T) {
	defer SnapshotStateForTests()()
	InitCurrency("AED")
	InitCurrencyDisplay("auto")
	// A code prefix is a leading symbol: the negative amount is isolated so
	// the sign stays left of the code (ut-docs#3880).
	got := FormatMoneyDisplay(-1250, "ar")
	want := "⁦-AED ١٢٫٥٠⁩"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	InitCurrencyDisplay("symbol")
	if got := FormatMoneyDisplay(-1250, "ar"); got != "-١٢٫٥٠ د.إ" {
		t.Errorf("symbol suffix must stay unwrapped: %q", got)
	}
}

func TestInitCurrencyDisplay_SnapshotRestores(t *testing.T) {
	InitCurrencyDisplay("code")
	restore := SnapshotStateForTests()
	InitCurrencyDisplay("symbol")
	restore()
	if got := CurrencyDisplayMode(); got != "code" {
		t.Errorf("restore: %q", got)
	}
	InitCurrencyDisplay("")
}

func TestFormatMoneyPrint_FallsBackToCodeWhenSymbolUnprintable(t *testing.T) {
	defer SnapshotStateForTests()()
	InitCurrency("AED")
	InitCurrencyDisplay("symbol")
	if got := FormatMoneyPrint(1250, "ar", "cp858"); got != "AED 12.50" {
		t.Errorf("unprintable symbol on cp858: %q", got)
	}
	if got := FormatMoneyPrint(1250, "ar", "utf8"); got != "AED 12.50" {
		t.Errorf("Arabic glyph is unprintable on a thermal head even under utf8: %q", got)
	}
	InitCurrencyDisplay("auto")
	if got := FormatMoneyPrint(-1250, "ar", "cp858"); got != "-AED 12.50" {
		t.Errorf("negative, no bidi controls: %q", got)
	}
	InitCurrency("GBP")
	if got := FormatMoneyPrint(-1250, "en", "cp858"); got != "-£12.50" {
		t.Errorf("GBP cp858: %q", got)
	}
	if got := FormatMoneyPrint(-1250, "en", "ascii"); got != "-GBP 12.50" {
		t.Errorf("GBP ascii: %q", got)
	}
	InitCurrency("TRY")
	InitCurrencyDisplay("symbol")
	if got := FormatMoneyPrint(1250, "tr", "cp858"); got != "TRY 12,50" {
		t.Errorf("TRY symbol on cp858: %q", got)
	}
}

// ut-docs#5410 review: a positive code-prefixed amount in an RTL locale needs
// the same isolate as a negative one, else it displays as "12.50 AED".
func TestFormatMoneyDisplay_RTLCodePrefixPositiveIsolated(t *testing.T) {
	defer SnapshotStateForTests()()
	InitCurrency("AED")
	InitCurrencyDisplay("auto")
	if got, want := FormatMoneyDisplay(1250, "ar"), "⁦AED ١٢٫٥٠⁩"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got, want := FormatMoneyDisplay(1250, "en"), "AED 12.50"; got != want {
		t.Errorf("LTR must stay plain: %q want %q", got, want)
	}
	InitCurrencyDisplay("symbol") // suffix symbol: unwrapped
	if got := FormatMoneyDisplay(1250, "ar"); got != "١٢٫٥٠ د.إ" {
		t.Errorf("suffix symbol: %q", got)
	}
	InitCurrency("JPY") // one-rune prefix symbol: unwrapped
	if got := FormatMoneyDisplay(300, "fa"); got != "¥۳۰۰" {
		t.Errorf("symbol positive: %q", got)
	}
}

// Review of ut-docs#5410: a named foreign currency in code mode is a code
// prefix too, so the on-screen form isolates it in RTL like the active one.
func TestFormatMoneyInDisplay_RTLIsolate(t *testing.T) {
	defer SnapshotStateForTests()()
	InitCurrency("AED")
	InitCurrencyDisplay("code")
	if got, want := FormatMoneyInDisplay(1250, "USD", "ar"), "⁦USD ١٢٫٥٠⁩"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got, want := FormatMoneyInDisplay(-1250, "USD", "ar"), "⁦-USD ١٢٫٥٠⁩"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got, want := FormatMoneyInDisplay(1250, "USD", "en"), "USD 12.50"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// Review: utf8 is "always encodable", so the Arabic dirham sign used to go to
// the printer raw on a default-charset AED shop.
func TestFormatMoneyPrint_ArabicSymbolFallsBackOnUTF8(t *testing.T) {
	defer SnapshotStateForTests()()
	InitCurrency("AED")
	InitCurrencyDisplay("symbol")
	for _, cs := range []string{"utf8", ""} {
		if got := FormatMoneyPrint(1250, "en", cs); got != "AED 12.50" {
			t.Errorf("charset %q: %q", cs, got)
		}
	}
	InitCurrency("EUR")
	if got := FormatMoneyPrint(1250, "en", "utf8"); got != "€12.50" {
		t.Errorf("non-Arabic symbol must be kept: %q", got)
	}
}
