package clock

import (
	"testing"
	"time"
)

func TestParsePinned(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantOK  bool
		wantStr string // RFC3339 of the parsed instant, when wantOK
	}{
		{"valid rfc3339 Z", "2026-01-02T15:04:05Z", true, "2026-01-02T15:04:05Z"},
		{"valid rfc3339 offset", "2026-01-02T15:04:05+02:00", true, "2026-01-02T15:04:05+02:00"},
		{"whitespace trimmed", "  2026-01-02T15:04:05Z  ", true, "2026-01-02T15:04:05Z"},
		{"empty", "", false, ""},
		{"garbage", "not-a-time", false, ""},
		{"date only (not rfc3339)", "2026-01-02", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parsePinned(c.in)
			if ok != c.wantOK {
				t.Fatalf("parsePinned(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			}
			if c.wantOK {
				want, err := time.Parse(time.RFC3339, c.wantStr)
				if err != nil {
					t.Fatalf("bad test fixture %q: %v", c.wantStr, err)
				}
				if !got.Equal(want) {
					t.Fatalf("parsePinned(%q) = %v, want %v", c.in, got, want)
				}
			}
		})
	}
}

// TestNowPinnedFromEnv exercises the real Now() path: with the env set to a
// fixed instant, Now() must return exactly that instant, repeatedly. Because
// Now() caches via sync.Once, the env must be set before the first call in
// this binary — do it here, before anything else touches Now().
func TestNowPinnedFromEnv(t *testing.T) {
	pinned := "2026-01-02T15:04:05Z"
	t.Setenv(DocsShotsNowEnv, pinned)

	want, _ := time.Parse(time.RFC3339, pinned)
	first := Now()
	if !first.Equal(want) {
		t.Fatalf("Now() with %s=%s = %v, want %v", DocsShotsNowEnv, pinned, first, want)
	}
	// Stable across calls (the whole point — a pinned clock does not advance).
	if second := Now(); !second.Equal(first) {
		t.Fatalf("Now() drifted: %v then %v", first, second)
	}
}
