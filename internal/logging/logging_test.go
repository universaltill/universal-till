package logging

import (
	"strings"
	"testing"
)

// The problems digest keeps only warn/error lines, newest first, capped.
func TestRecentKeepsWarnErrorNewestFirstCapped(t *testing.T) {
	l := L()
	l.Infof("just info %d", 1) // must not be remembered
	l.Warnf("warn one")
	l.Errorf("error two")

	got := Recent()
	if len(got) < 2 {
		t.Fatalf("recent = %d entries, want >= 2", len(got))
	}
	if got[0].Msg != "error two" || got[0].Level != "ERROR" {
		t.Fatalf("newest first: got %+v", got[0])
	}
	if got[1].Msg != "warn one" || got[1].Level != "WARN" {
		t.Fatalf("second: got %+v", got[1])
	}
	for _, p := range got {
		if strings.Contains(p.Msg, "just info") {
			t.Fatalf("info line remembered: %+v", p)
		}
	}

	for i := 0; i < recentCap+10; i++ {
		l.Warnf("flood %d", i)
	}
	if n := len(Recent()); n != recentCap {
		t.Fatalf("cap: %d entries, want %d", n, recentCap)
	}
}
