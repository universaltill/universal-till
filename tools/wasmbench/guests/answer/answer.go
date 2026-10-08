// Package answer is the event handler both bench guests share, so command and
// reactor mode time the same work: decode the event, encode a small policy
// answer of the size ut-plugin-tax-uk's charge.policy.ask returns.
package answer

import "encoding/json"

type event struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type policy struct {
	Country         string `json:"country"`
	ServiceCharge   bool   `json:"service_charge_allowed"`
	TipsVATExempt   bool   `json:"tips_vat_exempt"`
	MaxSuggestedBps int64  `json:"max_suggested_bps"`
	Note            string `json:"note"`
}

// Answer returns the JSON answer for a charge.policy.ask event, or false for
// any other event type.
func Answer(raw []byte) ([]byte, bool) {
	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Type != "charge.policy.ask" {
		return nil, false
	}
	out, err := Policy()
	return out, err == nil
}

// Policy is the encoded answer alone, for a guest whose event loop (the SDK's
// plugin.Run) already decoded and dispatched the event.
func Policy() ([]byte, error) {
	return json.Marshal(policy{Country: "GB", ServiceCharge: true, TipsVATExempt: true, MaxSuggestedBps: 1250, Note: "discretionary"})
}
