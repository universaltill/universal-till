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
	if !bytes.Contains(out, []byte("KITCHEN")) {
		t.Error("station header missing")
	}
	if !bytes.Contains(out, []byte("Fries")) {
		t.Error("qty-1 item row missing")
	}
	if bytes.Contains(out, []byte("1 x Fries")) {
		t.Error("qty-1 item must print without a quantity prefix")
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

// TestRenderKitchenTicketBlankOrderTypePrintsNothing covers ut-docs#261's
// product decision: dine-in (OrderType == "") prints NO order-type line at
// all -- not "DINE-IN", not an empty line, nothing -- distinguishing it
// from a translated-but-empty string would be a real regression this test
// must catch.
func TestRenderKitchenTicketBlankOrderTypePrintsNothing(t *testing.T) {
	tk := sampleTicket()
	tk.OrderType = ""

	out := RenderKitchenTicket(tk)
	if bytes.Contains(out, []byte("TAKEAWAY")) {
		t.Error("blank OrderType must not print the previous ticket's order type")
	}
	// The dashed separator must follow directly after the order/table/
	// timestamp block with nothing extra in between -- i.e. exactly the
	// same line count as a ticket with no OrderType field at all.
	withType := bytes.Count(RenderKitchenTicket(sampleTicket()), []byte("\n"))
	withoutType := bytes.Count(out, []byte("\n"))
	if withoutType != withType-1 {
		t.Errorf("blank OrderType should drop exactly one line, got %d lines vs %d with a type set", withoutType, withType)
	}
}

// ut-docs#1181 / ADR-0073 Decision 7: a per-line Mode marker prints under
// the item for a MIXED order; an empty Mode leaves the ticket byte-identical
// to before the field existed (every uniform sale).
func TestRenderKitchenTicket_LineModeMarkerOnlyWhenSet(t *testing.T) {
	base := KitchenTicket{OrderNo: "R-1", Items: []KitchenItem{{Qty: "1", Name: "Coffee"}}}
	withMode := KitchenTicket{OrderNo: "R-1", Items: []KitchenItem{{Qty: "1", Name: "Coffee", Mode: "Takeaway"}}}
	if a, b := RenderKitchenTicket(base), RenderKitchenTicket(KitchenTicket{OrderNo: "R-1", Items: []KitchenItem{{Qty: "1", Name: "Coffee", Mode: ""}}}); string(a) != string(b) {
		t.Fatal("an empty Mode must not change the ticket bytes")
	}
	if strings.Contains(string(RenderKitchenTicket(base)), "* ") {
		t.Fatal("a line without a Mode must carry no mode marker")
	}
	if !strings.Contains(string(RenderKitchenTicket(withMode)), "* TAKEAWAY") {
		t.Fatal("ESC/POS ticket must carry the mode marker too")
	}
}

func dirtyTicket(f func(string) string, cs string) KitchenTicket {
	in := func(s string) string { return f(s + kick + forged) }
	return KitchenTicket{
		Station: in("KITCHEN"), OrderNo: in("R-1"), OrderLabel: in("ORDER"), OrderType: in("takeaway"),
		Table: in("T5"), Timestamp: in("18:30"), Charset: cs,
		Items: []KitchenItem{{Qty: in("2"), Name: in("Burger"), Modifiers: []string{in("no onions")}, Mode: in("takeaway")}},
	}
}

func TestRenderKitchenTicket_ControlRunesNeverReachPrinter(t *testing.T) {
	ident := func(s string) string { return s }
	for _, cs := range allCharsets {
		out := RenderKitchenTicket(dirtyTicket(ident, cs))
		if n := bytes.Count(out, []byte(escP)); n != 0 {
			t.Errorf("%q: ESC p x%d", cs, n)
		}
		if want := RenderKitchenTicket(dirtyTicket(twinOf, cs)); !bytes.Equal(out, want) {
			t.Errorf("%q: differs from twin\n got %q\nwant %q", cs, out, want)
		}
		clean := RenderKitchenTicket(dirtyTicket(func(string) string { return "x" }, cs))
		if a, b := bytes.Count(out, []byte("\n")), bytes.Count(clean, []byte("\n")); a != b {
			t.Errorf("%q: %d newlines, clean ticket has %d", cs, a, b)
		}
	}
}
