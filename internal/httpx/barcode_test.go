package httpx

import (
	"strings"
	"testing"
)

func TestBarcodeSVG(t *testing.T) {
	svg := string(BarcodeSVG("000000037"))
	if !strings.Contains(svg, "<svg") || !strings.Contains(svg, "rect") {
		t.Fatal("no svg produced for a valid receipt number")
	}
	if !strings.Contains(svg, `aria-label="000000037"`) {
		t.Error("aria label missing")
	}
	// (start* + 9 digits + stop*) × 5 bars each = 55 bar rects + 1 bg rect.
	if got := strings.Count(svg, "<rect"); got != 56 {
		t.Errorf("rect count = %d, want 56", got)
	}
	if BarcodeSVG("ABC") != "" || BarcodeSVG("") != "" {
		t.Error("non-digit/empty input must render nothing")
	}
}
