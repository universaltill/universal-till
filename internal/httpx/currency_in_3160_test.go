package httpx

import "testing"

// ut-docs#3160: a plugin view's money cell carries its own currency.
func TestFormatMoneyIn_3160(t *testing.T) {
	if got := FormatMoneyIn(123456, "EUR", "de-DE"); got != "€1.234,56" && got != "1.234,56 €" {
		t.Errorf("EUR de-DE = %q", got)
	}
	if got := FormatMoneyIn(1234, "JPY", "en"); got != "¥1,234" {
		t.Errorf("JPY = %q, want ¥1,234 (0 decimals)", got)
	}
	if got := FormatMoneyIn(-150, "GBP", "en-GB"); got != "-£1.50" && got != "£-1.50" {
		t.Errorf("negative GBP = %q", got)
	}
	if got := FormatDecimal("-1234567.25", "en"); got != "-1,234,567.25" {
		t.Errorf("FormatDecimal en = %q", got)
	}
	if got := FormatDecimal("1234", "fa"); got != "۱٬۲۳۴" {
		t.Errorf("FormatDecimal fa = %q", got)
	}
}
