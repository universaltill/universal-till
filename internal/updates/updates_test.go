package updates

import "testing"

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.3", "0.1.2", true},
		{"0.1.2", "0.1.2", false},
		{"0.1.2", "0.1.3", false},
		{"1.0.0", "0.9.9", true},
		{"0.2.0", "0.1.9", true},
		{"0.1.10", "0.1.9", true}, // numeric, not lexical
		{"0.1.3", "dev", true},    // any release beats a dev build
		{"0.1.3", "", true},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
