package print

import (
	"context"
	"strings"
	"testing"
)

// A regular (system) printer must receive PLAIN TEXT — no ESC/POS control
// bytes, which is exactly what made an HP print garbage / one line each. The
// system path renders via RenderText, so its output must be clean.
func TestSystemRenderIsPlainText(t *testing.T) {
	doc := Doc{
		StoreName: "Ali's Shop",
		Meta:      []string{"Receipt #42"},
		Lines:     []Line{{Name: "Coffee", Qty: "2", Amount: "5.00"}},
		Totals:    []KV{{Label: "Total", Amount: "5.00", Strong: true}},
		Footer:    []string{"Thank you"},
	}
	text := RenderText(doc)
	if strings.ContainsRune(text, 0x1b) { // ESC
		t.Fatal("system text must not contain ESC control bytes")
	}
	if strings.ContainsRune(text, 0x1d) { // GS
		t.Fatal("system text must not contain GS control bytes")
	}
	for _, want := range []string{"Ali's Shop", "Coffee", "Total"} {
		if !strings.Contains(text, want) {
			t.Fatalf("system text missing %q:\n%s", want, text)
		}
	}
}

// PrintDoc is a no-op for an unconfigured printer (never errors, never prints).
func TestPrintDocOffIsNoop(t *testing.T) {
	for _, mode := range []string{"", "off"} {
		if err := PrintDoc(context.Background(), Config{Mode: mode}, Doc{StoreName: "x"}); err != nil {
			t.Fatalf("mode %q: PrintDoc = %v, want nil", mode, err)
		}
	}
}
