package print

import (
	"bytes"
	"strings"
	"testing"
)

func sampleTicket() KitchenTicket {
	return KitchenTicket{
		Station:   "KITCHEN",
		OrderNo:   "R-0042",
		OrderType: "takeaway",
		Table:     "Table 5",
		Timestamp: "2026-07-16 18:30",
		Items: []KitchenItem{
			{Qty: "2", Name: "Cheeseburger", Modifiers: []string{"no onions", "extra cheese"}},
			{Qty: "1", Name: "Fries"},
		},
	}
}

func TestRenderKitchenTicketStructure(t *testing.T) {
	out := RenderKitchenTicket(sampleTicket())
	if !bytes.HasPrefix(out, cmdInit) {
		t.Error("stream must start with ESC @ init")
	}
	if !bytes.HasSuffix(out, cmdFeedCut) {
		t.Error("stream must end with feed+cut")
	}
	if bytes.Contains(out, cmdKickDrawer) {
		t.Error("kitchen ticket must never kick the drawer")
	}
	if !bytes.Contains(out, []byte("ORDER R-0042")) {
		t.Error("order number missing")
	}
	if !bytes.Contains(out, []byte("2 x Cheeseburger")) {
		t.Error("qty x name row missing")
	}
	if !bytes.Contains(out, []byte("- no onions")) {
		t.Error("modifier row missing")
	}
	if !bytes.Contains(out, []byte("TAKEAWAY")) {
		t.Error("order type missing")
	}
}

func TestRenderKitchenTicketNoPrices(t *testing.T) {
	tk := sampleTicket()
	// Whatever the items, a kitchen ticket must not carry a currency figure.
	out := string(RenderKitchenTicket(tk))
	for _, sym := range []string{"£", "$", "€", ".00", ".80"} {
		if strings.Contains(out, sym) {
			t.Errorf("kitchen ticket must not contain price marker %q", sym)
		}
	}
}

func TestRenderKitchenTicketTextMirrors(t *testing.T) {
	txt := RenderKitchenTicketText(sampleTicket())
	for _, want := range []string{"KITCHEN", "ORDER R-0042", "2 x Cheeseburger", "- no onions", "Fries"} {
		if !strings.Contains(txt, want) {
			t.Errorf("preview text missing %q", want)
		}
	}
	// Plain text must carry no ESC/POS control bytes.
	if strings.ContainsRune(txt, 0x1b) || strings.ContainsRune(txt, 0x1d) {
		t.Error("preview text must not contain ESC/POS control bytes")
	}
}
