package buildinfo

import "testing"

func TestNewerThan(t *testing.T) {
	const built = "2026-10-15T12:00:00Z"
	cases := []struct {
		name, gen, build string
		want             bool
	}{
		{"bundle after build", "2026-10-16T00:00:00Z", built, true},
		{"bundle before build", "2026-10-01T00:00:00Z", built, false},
		{"same instant is not newer", built, built, false},
		{"offsets compare as instants", "2026-10-15T13:30:00+01:00", built, true},
		{"no generated_at", "", built, false},
		{"garbage generated_at", "yesterday", built, false},
		{"source build: every dated bundle is newer", "2020-01-01T00:00:00Z", "", true},
		{"source build: undated bundle still not newer", "", "", false},
	}
	for _, c := range cases {
		if got := newerThan(c.gen, c.build); got != c.want {
			t.Errorf("%s: newerThan(%q, %q) = %v, want %v", c.name, c.gen, c.build, got, c.want)
		}
	}
}
