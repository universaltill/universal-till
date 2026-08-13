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
