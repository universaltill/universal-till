package restorenum

import "testing"

func TestParseFloor(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"11000", 11000, true},
		{"1000000000000000", Max, true},
		{"1000000000000001", 0, false},
		{"9223372036854775807", 0, false},
		{"99999999999999999999", 0, false},
		{"-1", 0, false},
		{"-50", 0, false},
		{"", 0, false},
		{"abc", 0, false},
		{"1e18", 0, false},
		{"12abc", 0, false},
		{" 5", 0, false},
		{"1.5", 0, false},
	} {
		got, ok := ParseFloor(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseFloor(%q) = %d,%v want %d,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}
