package pluginview

import (
	"strings"
	"testing"
)

// The sell-screen overlay seam (ADR-0121 amendment 2026-11-28, ut-docs#7206):
// a page view may add basket lines with an opaque per-line ref.

func overlayCtx(viewSales bool) Context {
	c := seamCtx(SeamSellOverlay)
	c.ViewSales = viewSales
	return c
}

const refItem = `{"label":{"literal":"Skin fade"},"effect":{"add_to_basket":{"sku":"SVC-FADE","qty":1,"ref":"appt:7f3a:1"}}}`

func TestOverlaySeam_RefAndFullPage_7206(t *testing.T) {
	// A full page (text + a button-free table is enough) holds suggestions.
	raw := doc(`{"type":"text","text":{"literal":"Sam, 10:30"}},{"type":"suggestions","items":[` + refItem + `]}`)
	d, err := DecodeViewAnswer([]byte(raw), overlayCtx(true))
	if err != nil {
		t.Fatalf("overlay suggestions with ref refused: %v", err)
	}
	got := d.Prepare("en").Components[1].Suggestions[0]
	if got.SKU != "SVC-FADE" || got.Qty != 1 || got.Ref != "appt:7f3a:1" {
		t.Errorf("prepared = %+v, want sku SVC-FADE qty 1 ref appt:7f3a:1", got)
	}
}

func TestOverlaySeam_Refusals_7206(t *testing.T) {
	cases := []struct {
		name, item string
		viewSales  bool
		want       string
	}{
		{"no view:sales", refItem, false, "view:sales"},
		{"apply_fields", fieldsItem, true, "apply_fields"},
		{"empty ref", `{"label":{"literal":"x"},"effect":{"add_to_basket":{"sku":"A","ref":""}}}`, true, "ref"},
		{"ref too long", `{"label":{"literal":"x"},"effect":{"add_to_basket":{"sku":"A","ref":"` + strings.Repeat("a", 65) + `"}}}`, true, "ref"},
		{"ref with space (could hold a name)", `{"label":{"literal":"x"},"effect":{"add_to_basket":{"sku":"A","ref":"Sam Smith"}}}`, true, "ref"},
		{"ref with @", `{"label":{"literal":"x"},"effect":{"add_to_basket":{"sku":"A","ref":"a@b.c"}}}`, true, "ref"},
		{"ref not a string", `{"label":{"literal":"x"},"effect":{"add_to_basket":{"sku":"A","ref":5}}}`, true, "ref"},
		{"price field", `{"label":{"literal":"x"},"effect":{"add_to_basket":{"sku":"A","price":1}}}`, true, "price"},
	}
	for _, tc := range cases {
		_, err := DecodeViewAnswer([]byte(sugg(tc.item)), overlayCtx(tc.viewSales))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// A ref outside the overlay stays refused: the camera seam has no per-line
// ref, a page or slot has no suggestions at all.
func TestOverlaySeam_RefElsewhere_7206(t *testing.T) {
	if _, err := DecodeViewAnswer([]byte(sugg(refItem)), seamCtx(SeamSellIdentify)); err == nil || !strings.Contains(err.Error(), "ref") {
		t.Errorf("identify seam with ref: err = %v, want a ref refusal", err)
	}
	if _, err := DecodeViewAnswer([]byte(sugg(refItem)), testCtx); err == nil {
		t.Error("plain page accepted suggestions")
	}
}

// No ref = unchanged for the existing seam.
func TestOverlaySeam_NoRefUnchanged_7206(t *testing.T) {
	d, err := DecodeViewAnswer([]byte(sugg(basketItem)), overlayCtx(true))
	if err != nil {
		t.Fatalf("overlay no-ref refused: %v", err)
	}
	if r := d.Prepare("en").Components[0].Suggestions[0].Ref; r != "" {
		t.Errorf("ref = %q, want empty", r)
	}
}
