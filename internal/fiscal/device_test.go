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
