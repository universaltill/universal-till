package print

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
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

	txt := RenderKitchenTicketText(tk)
	if strings.Contains(txt, "TAKEAWAY") {
		t.Error("preview text: blank OrderType must not print the previous ticket's order type")
	}
	// Line-count check mirrored for the text-preview path too (review
	// finding, ut-docs#261): the byte path's guard alone doesn't prove the
	// text path also skips the line entirely rather than printing a blank
	// centered row — bytes.Contains("TAKEAWAY") above would pass either way.
	withTypeTxt := strings.Count(RenderKitchenTicketText(sampleTicket()), "\n")
	withoutTypeTxt := strings.Count(txt, "\n")
	if withoutTypeTxt != withTypeTxt-1 {
		t.Errorf("preview text: blank OrderType should drop exactly one line, got %d lines vs %d with a type set", withoutTypeTxt, withTypeTxt)
	}
}

// ut-docs#376: RenderKitchenTicketText's center() helper must center on
// rune count, not byte length — a multi-byte station/table name (e.g. an
// ä/ö/ü/ß or ar/fa/tr string) would otherwise pad as if it were one column
// wider than it visibly is, drifting off true center.
func TestRenderKitchenTicketTextCentersByRuneCountNotByteCount(t *testing.T) {
	tk := sampleTicket()
	station := "Grill Café Français" // 19 runes, 21 bytes (é, ç each +1 byte)
	tk.Station = station
	out := RenderKitchenTicketText(tk)

	wantPad := (Width - utf8.RuneCountInString(station)) / 2
	gotPad := kitchenLeadingSpaces(t, out, station)
	if gotPad != wantPad {
		t.Errorf("station name should center on rune count: want %d leading spaces, got %d", wantPad, gotPad)
	}
}

func kitchenLeadingSpaces(t *testing.T, out string, want string) int {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, want) {
			return len(line) - len(strings.TrimLeft(line, " "))
		}
	}
	t.Fatalf("line containing %q not found in output", want)
	return -1
}
