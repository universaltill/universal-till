package fiscal

import (
	"encoding/json"
	"testing"
)

func TestParseDeviceEvidence(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"empty", "", false},
		{"not json", "approved", false},
		{"no field", `{"status":"approved"}`, false},
		{"null field", `{"status":"approved","fiscal_device":null}`, false},
		{"no receipt no", `{"status":"approved","fiscal_device":{"serial":"AV1"}}`, false},
		{"blank receipt no", `{"fiscal_device":{"receipt_no":"   "}}`, false},
		// \u200b is ZERO WIDTH SPACE and \ufeff is the BOM/ZWNBSP (JSON
		// Unicode category Cf (format), which unicode.IsSpace/TrimSpace do
		// not treat as whitespace. ut-docs#1781: a receipt_no made up of
		// only these must still count as blank.
		{"zero-width-space-only receipt no", `{"fiscal_device":{"receipt_no":"\u200b\u200b"}}`, false},
		{"BOM-only receipt no", `{"fiscal_device":{"receipt_no":"\ufeff"}}`, false},
		{"valid", `{"status":"approved","fiscal_device":{"maker":"beko","serial":"AV1","receipt_no":" 000123 ","z_no":4,"issued_at":"2026-09-03T10:00:00+03:00"}}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, ok := ParseDeviceEvidence(json.RawMessage(c.in))
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (ev=%+v)", ok, c.ok, ev)
			}
			if !ok && ev != nil {
				t.Fatalf("absent evidence must be nil, got %+v", ev)
			}
		})
	}
	ev, _ := ParseDeviceEvidence(json.RawMessage(`{"fiscal_device":{"receipt_no":" 000123 ","z_no":4}}`))
	if ev.ReceiptNo != "000123" || ev.Kind != "okc" || ev.ZNo != 4 {
		t.Fatalf("normalisation: %+v", ev)
	}
}

// TestParseDeviceEvidenceStripsSandwichedInvisibleChars covers ut-docs#1790:
// unlike ut-docs#1781's all-invisible case (correctly rejected as blank by
// isBlankReceiptNo), a receipt_no with real digits SANDWICHED between
// zero-width/format characters at its edges is valid evidence — but must
// still have those edge characters stripped before persisting, or the
// stored value won't exact-match a freshly typed/scanned "123" on a
// reprint-by-receipt-number or lookup path. \u200b is ZERO WIDTH SPACE
// and \ufeff is the BOM/ZWNBSP, the same Cf characters ut-docs#1781
// already covers for the all-invisible case above.
func TestParseDeviceEvidenceStripsSandwichedInvisibleChars(t *testing.T) {
	// Leading ordinary space + ZWSP, trailing BOM + space + ZWSP, real
	// digits sandwiched in between — the edges must be stripped, the
	// interior digits must survive untouched.
	in := `{"fiscal_device":{"receipt_no":" \u200b123\ufeff \u200b"}}`
	ev, ok := ParseDeviceEvidence(json.RawMessage(in))
	if !ok {
		t.Fatalf("ok = false, want true (evidence with real digits must be valid)")
	}
	if ev.ReceiptNo != "123" {
		t.Fatalf("ReceiptNo = %q, want %q (invisible/whitespace edges must be stripped)", ev.ReceiptNo, "123")
	}
}
