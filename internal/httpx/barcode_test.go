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
	if BarcodeSVG("~") != "" || BarcodeSVG("abc") != "" || BarcodeSVG("") != "" {
		t.Error("unencodable/empty input must render nothing")
	}
	// Invoice numbers carry letters and a dash (review find: these
	// rendered BLANK when only digits were mapped).
	inv := string(BarcodeSVG("T2-INV-000001"))
	if !strings.Contains(inv, "<svg") || !strings.Contains(inv, `aria-label="T2-INV-000001"`) {
		t.Fatal("invoice display number did not render")
	}
}
