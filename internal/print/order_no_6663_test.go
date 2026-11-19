package print

import (
	"bytes"
	"strings"
	"testing"
)

// ut-docs#6663: the order number is printed as its own centred block under
// the store header, double size and bold, label on a normal line above it.
func TestRender_OrderNoBlockIsDoubleSizeUnderTheHeader(t *testing.T) {
	d := sampleDoc()
	d.OrderNo = "1234" // 4 digits must still fit at double width
	d.OrderLabel = "Order number"
	out := Render(d)
	big := append(append(append([]byte{}, cmdBoldOn...), cmdDoubleOn...), []byte("1234\n")...)
	i := bytes.Index(out, big)
	if i < 0 {
		t.Fatalf("no bold double-size order number in the stream: %q", out)
	}
	if lbl := bytes.Index(out, []byte("Order number\n")); lbl < 0 || lbl > i {
		t.Fatalf("label must be on its own line above the number (label at %d, number at %d)", lbl, i)
	}
	if store := bytes.Index(out, []byte("Test Shop")); store > i {
		t.Fatal("order number must come after the store header")
	}
	if rcpt := bytes.Index(out, []byte("Receipt R-0001")); rcpt < i {
		t.Fatal("receipt number (Meta) must stay below the order block")
	}
	// Double width halves the columns: an over-long value is clipped to
	// Width/2 so it can never wrap onto a second double-size row.
	long := Render(Doc{StoreName: "Shop", OrderNo: strings.Repeat("9", Width)})
	if !bytes.Contains(long, []byte(strings.Repeat("9", Width/2))) || bytes.Contains(long, []byte(strings.Repeat("9", Width/2+1))) {
		t.Fatalf("an over-long order number must be clipped to %d columns", Width/2)
	}
}

func TestRender_NoOrderNoMeansNoBlock(t *testing.T) {
	d := sampleDoc()
	with := d
	with.OrderNo, with.OrderLabel = "7", "Order number"
	if bytes.Equal(Render(d), Render(with)) {
		t.Fatal("an order number must change the output")
	}
	if bytes.Contains(Render(d), []byte("Order number")) {
		t.Fatal("no order number: no label either")
	}
}

func TestRender_ReprintKeepsBothNumbers(t *testing.T) {
	d := sampleDoc()
	d.OrderNo, d.OrderLabel = "42", "Order number"
	a, b := Render(d), Render(d)
	if !bytes.Equal(a, b) {
		t.Fatal("reprint must be byte-identical")
	}
	for _, want := range []string{"42\n", "Receipt R-0001"} {
		if !bytes.Contains(a, []byte(want)) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestRenderText_OrderNoCentredUnderHeader(t *testing.T) {
	d := sampleDoc()
	d.OrderNo, d.OrderLabel = "42", "Order number"
	txt := string(RenderText(d))
	if !strings.Contains(txt, "Order number") || !strings.Contains(txt, "42\n") {
		t.Fatalf("text receipt lacks the order block: %q", txt)
	}
	if strings.Index(txt, "42\n") > strings.Index(txt, "Receipt R-0001") {
		t.Fatal("order block must come before the receipt number")
	}
}

func TestRenderKitchenTicket_OrderNoDoubleSizeNoPrefix(t *testing.T) {
	tk := sampleTicket()
	out := RenderKitchenTicket(tk)
	big := append(append([]byte{}, cmdDoubleOn...), []byte("42\n")...)
	if !bytes.Contains(out, big) {
		t.Fatalf("order number is not in double size: %q", out)
	}
}
