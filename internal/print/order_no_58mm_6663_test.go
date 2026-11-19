package print

import (
	"bytes"
	"testing"
)

// ut-docs#6663 (UX gate): the renderer is a fixed 42-column (80mm) layout.
// The order block must still be legal on a 58mm roll (32 cols normal, 16 at
// double width): longest label (de) on its own normal line, a 4-digit number
// on the double-size row, the longest receipt number (3-letter series + 9
// digits) on one normal row. This does NOT prove the rest of the receipt
// fits 32 columns (it does not; 58mm is a printer-side concern today).
func TestRender_OrderBlockFits58mmColumns(t *testing.T) {
	d := sampleDoc()
	d.OrderNo, d.OrderLabel = "9999", "Bestellnummer"
	d.Meta = []string{"Receipt AAA-000000001"}
	out := Render(d)
	big := append(append(append([]byte{}, cmdBoldOn...), cmdDoubleOn...), []byte("9999\n")...)
	if !bytes.Contains(out, big) {
		t.Fatalf("4-digit number not intact on the double-size row: %q", out)
	}
	for _, row := range bytes.Split(out, []byte("\n")) {
		if bytes.Contains(row, []byte("Bestellnummer")) && len(bytes.TrimSpace(row)) > 32+8 {
			t.Fatalf("label row too long for 58mm: %q", row)
		}
		if bytes.Contains(row, []byte("AAA-000000001")) && len(bytes.TrimSpace(row)) > 32 {
			t.Fatalf("receipt number row too long for 58mm: %q", row)
		}
	}
	if len("Receipt AAA-000000001") > 32 || len("9999") > 16 || len("Bestellnummer") > 32 {
		t.Fatal("58mm budget exceeded")
	}
}
