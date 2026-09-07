package fiscal

import (
	"encoding/json"
	"strings"
)

// Fiscal *device* markets — Turkey's YN ÖKC (Law No. 3100) today.
//
// Germany's regime (ADR-0044) has the till print the receipt and a TSE sign
// it after payment, so `fiscal.sign.ask` fires post-authorize and may
// proceed-and-declare on failure. Turkey's regime is the other way round:
// the certified device takes the payment AND prints the legal receipt
// (mali fiş), so the device must be reached AT tender and a sale cannot
// complete without it. That is exactly the blocking
// `payment.<key>.authorize` seam every card-terminal plugin already uses
// (ut-docs reference/payment-provider-contract.md): the ÖKC plugin is a
// payment method ("pay on the device"), a non-zero exit declines the
// tender and no sale row is ever created — fail-closed by construction,
// no override path. See docs/arch/turkey-launch-playbook.md E1/E3.
//
// This file holds the additive answer contract that makes the device's
// receipt evidence flow back into core: an "approved" authorize/refund
// answer MAY carry a `fiscal_device` object, persisted verbatim
// (data.FiscalDeviceReceipt) and shown on the till's own receipt copy.

// PluginIDTaxTR is the Turkish fiscal-device plugin's manifest id (ADR-0009
// naming, sibling of com.universaltill.tax-de). Core keys the Turkey
// status page and its nav tile on this plugin being installed and active.
const PluginIDTaxTR = "com.universaltill.tax-tr"

// DeviceEvidence is the `fiscal_device` object a fiscal-device payment
// plugin returns alongside an approved tender: what the device printed.
// Every field is optional on the wire; the object counts as PRESENT only
// when ReceiptNo is non-empty (Valid) — a receipt number is the one thing
// a printed fiş always has, so evidence without it proves nothing.
type DeviceEvidence struct {
	Kind        string `json:"kind,omitempty"`
	Maker       string `json:"maker,omitempty"`
	Serial      string `json:"serial,omitempty"`
	ReceiptNo   string `json:"receipt_no,omitempty"`
	ReceiptKind string `json:"receipt_kind,omitempty"`
	ZNo         int64  `json:"z_no,omitempty"`
	IssuedAt    string `json:"issued_at,omitempty"`
}

// Valid reports whether the evidence carries the one field that makes it
// evidence: a device receipt number.
func (e *DeviceEvidence) Valid() bool {
	return e != nil && strings.TrimSpace(e.ReceiptNo) != ""
}

// ParseDeviceEvidence extracts the optional `fiscal_device` object from a
// payment plugin's raw authorize/refund answer. ok is false — evidence
// absent, nothing to persist — when resp is empty, not a JSON object,
// lacks the field, or the object has no receipt number. A plugin bug here
// must never invent evidence, only ever report a real receipt, so every
// malformed shape degrades to "absent" rather than an error.
func ParseDeviceEvidence(resp json.RawMessage) (*DeviceEvidence, bool) {
	if len(resp) == 0 {
		return nil, false
	}
	var parsed struct {
		FiscalDevice *DeviceEvidence `json:"fiscal_device"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil || !parsed.FiscalDevice.Valid() {
		return nil, false
	}
	ev := parsed.FiscalDevice
	ev.ReceiptNo = strings.TrimSpace(ev.ReceiptNo)
	if ev.Kind == "" {
		ev.Kind = "okc"
	}
	return ev, true
}
