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
