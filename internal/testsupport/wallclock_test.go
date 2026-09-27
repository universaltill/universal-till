package testsupport

import (
	"testing"
	"time"
)

// TestWallStepped is WallStepped's own sanity check: if Go's time.Time
// layout ever changes, this fails loudly instead of every test built on
// WallStepped failing in confusing ways.
func TestWallStepped(t *testing.T) {
	now := time.Now()
	for _, d := range []time.Duration{time.Hour, -73 * time.Second} {
		stepped := WallStepped(now, d)
		if got := stepped.Round(0).Sub(now.Round(0)); got != d {
			t.Fatalf("WallStepped(%v): wall clock moved by %v, want %v", d, got, d)
		}
		if got := stepped.Sub(now); got != 0 {
			t.Fatalf("WallStepped(%v): monotonic reading moved by %v, want 0", d, got)
		}
	}
}
