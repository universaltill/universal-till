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
	}
	for _, c := range cases {
		if got := FormatPercent(c.bp); got != c.want {
			t.Errorf("FormatPercent(%d) = %q, want %q", c.bp, got, c.want)
		}
	}
}
