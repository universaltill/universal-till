package taxrate

import "testing"

func TestFormatPercent(t *testing.T) {
	cases := []struct {
		bp   int
		want string
	}{
		{1900, "19"},
		{1950, "19.5"},
		{0, "0"},
		{-500, "-5"},
		{-750, "-7.5"},
		{125, "1.25"},
		{1905, "19.05"}, // leading-zero fractional digit: pins the %02d padding
		{50, "0.5"},     // sub-1% value: zero integer part
		{5, "0.05"},     // sub-1% value with a leading-zero fractional digit too
		{10000, "100"},  // upper bound ParseTaxRateBP permits (100%)
	}
	for _, c := range cases {
		if got := FormatPercent(c.bp); got != c.want {
			t.Errorf("FormatPercent(%d) = %q, want %q", c.bp, got, c.want)
		}
	}
}

// ut-docs#3259: ParsePercent is FormatPercent's inverse for the shop's
// default VAT rate (store.tax_rate) — integer-exact, '.' or ',' separator,
// at most two decimals, 0–100 %.
func TestParsePercent(t *testing.T) {
	ok := []struct {
		in   string
		want int
	}{
		{"19", 1900},
		{"8.1", 810},
		{"8,1", 810},
		{"8,10", 810},
		{"5.55", 555},
		{"5.5", 550},
		{"13.5", 1350},
		{"0", 0},
		{"0.5", 50},
		{"0.05", 5},
		{"100", 10000},
		{"100.00", 10000},
		{"025", 2500},
		{" 20 ", 2000},
		{"19.", 1900}, // trailing separator, no fraction digits: harmless
	}
	for _, c := range ok {
		got, valid := ParsePercent(c.in)
		if !valid || got != c.want {
			t.Errorf("ParsePercent(%q) = %d, %v; want %d, true", c.in, got, valid, c.want)
		}
	}
	bad := []string{
		"", "   ", "8.125", "101", "100.01", "-1", "+5", "abc", "1e3", "NaN", "Inf",
		".5", ",5", "8.1.1", "8,1,1", "8.1%", "8 .1", "١٩", "99999999999999999999",
	}
	for _, in := range bad {
		if got, valid := ParsePercent(in); valid {
			t.Errorf("ParsePercent(%q) = %d, true; want !ok", in, got)
		}
	}
}

// Every value in range survives FormatPercent → ParsePercent unchanged.
func TestParsePercent_RoundTripsFormatPercent(t *testing.T) {
	for bp := 0; bp <= 10000; bp++ {
		got, ok := ParsePercent(FormatPercent(bp))
		if !ok || got != bp {
			t.Fatalf("ParsePercent(FormatPercent(%d)=%q) = %d, %v", bp, FormatPercent(bp), got, ok)
		}
	}
}
