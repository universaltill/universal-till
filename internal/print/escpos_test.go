package print

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func sampleDoc() Doc {
	return Doc{
		StoreName: "Test Shop",
		Meta:      []string{"Receipt R-0001", "2026-07-14 12:00"},
		Lines: []Line{
			{Name: "Coca-Cola Can 330ml", Qty: "2", Amount: "£2.80"},
			{Name: "A very long product name that will not fit on one line at all", Qty: "1", Amount: "£1.00"},
		},
		Totals: []KV{
			{Label: "Subtotal", Amount: "£3.80"},
			{Label: "TOTAL", Amount: "£3.80", Strong: true},
		},
		Payments: []KV{{Label: "Cash", Amount: "£5.00"}, {Label: "Change", Amount: "£1.20"}},
		Footer:   []string{"Thank you!"},
	}
}

func TestRenderStructure(t *testing.T) {
	out := Render(sampleDoc())
	if !bytes.HasPrefix(out, cmdInit) {
		t.Error("stream must start with ESC @ init")
	}
	if !bytes.HasSuffix(out, cmdFeedCut) {
		t.Error("stream must end with feed+cut")
	}
	if bytes.Contains(out, cmdKickDrawer) {
		t.Error("drawer must not kick unless asked")
	}
	if !bytes.Contains(out, []byte("2 x Coca-Cola Can 330ml")) {
		t.Error("qty x name row missing")
	}
	// Amount right-aligned on a 42-char row.
	rows := strings.Split(string(out), "\n")
	found := false
	for _, r := range rows {
		if strings.Contains(r, "2 x Coca-Cola") && strings.HasSuffix(r, "£2.80") && utf8.RuneCountInString(r) >= Width {
			found = true
		}
	}
	if !found {
		t.Error("line row not right-aligned to width")
	}
}

func TestRenderBarcode(t *testing.T) {
	d := sampleDoc()
	d.Barcode = "000000042"
	out := Render(d)
	// GS k 73 <len> { B <data> — CODE128 code set B of the receipt no.
	want := append([]byte{0x1d, 0x6b, 0x49, byte(len("{B000000042"))}, []byte("{B000000042")...)
	if !bytes.Contains(out, want) {
		t.Error("CODE128 barcode sequence missing")
	}
	if !bytes.Contains(out, cmdBarcodeHRI) {
		t.Error("HRI (number under the bars) missing")
	}
	// No barcode block when empty or unencodable.
	d.Barcode = ""
	if bytes.Contains(Render(d), cmdBarcodeCode128) {
		t.Error("barcode printed for empty code")
	}
	d.Barcode = "رسید"
	if bytes.Contains(Render(d), cmdBarcodeCode128) {
		t.Error("barcode printed for non-ASCII code")
	}
}

func TestRenderKickDrawer(t *testing.T) {
	d := sampleDoc()
	d.KickDrawer = true
	if !bytes.Contains(Render(d), cmdKickDrawer) {
		t.Error("drawer kick missing")
	}
}

func TestLongNameWraps(t *testing.T) {
	out := string(Render(sampleDoc()))
	if !strings.Contains(out, "A very long product name") {
		t.Error("long name row missing")
	}
	// its amount lands on its own right-aligned row, padded to exactly
	// Width visible columns. Compares the exact row (not a Contains
	// substring check, which can't distinguish "close enough" from
	// "exact" -- a run of N spaces trivially contains any shorter run of
	// the same character regardless of alignment, so this used to pass
	// for the wrong reason; ut-docs#438), and uses rune count rather than
	// byte length for the padding width, matching what kvRow itself pads
	// to (ut-docs#376).
	amount := "£1.00"
	wantRow := strings.Repeat(" ", Width-utf8.RuneCountInString(amount)) + amount
	found := false
	for _, row := range strings.Split(out, "\n") {
		if row == wantRow {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("wrapped amount row missing or not exactly padded to width; want row %q", wantRow)
	}
}

// TestLayoutLineRuneBoundaryFitsOneRow: a multi-byte label whose BYTE length
// exceeds Width but whose RUNE count does not must stay on one row --
// layoutLine's one-row/two-row threshold has to agree with kvRow's own
// rune-based padding, or a label right at this boundary gets routed to an
// unnecessary two-row fallback even though kvRow can render it on one row
// fine (ut-docs#438; kvRow's own rune-safety fixed by ut-docs#376).
func TestLayoutLineRuneBoundaryFitsOneRow(t *testing.T) {
	amount := "£1.00"                // 5 runes, 6 bytes
	label := strings.Repeat("é", 20) // 20 runes, 40 bytes
	// rune total: 20 + 1 + 5 = 26 <= Width(42)  -> fits on one row
	// byte total:  40 + 1 + 6 = 47 >  Width(42) -> byte-based math would wrap
	if got := utf8.RuneCountInString(label) + 1 + utf8.RuneCountInString(amount); got > Width {
		t.Fatalf("test setup invalid: rune total %d exceeds Width %d", got, Width)
	}
	if got := len(label) + 1 + len(amount); got <= Width {
		t.Fatalf("test setup invalid: byte total %d does not exceed Width %d", got, Width)
	}

	rows := layoutLine(Line{Name: label, Qty: "1", Amount: amount})
	if len(rows) != 1 {
		t.Fatalf("want 1 row for a label that fits by rune count, got %d: %q", len(rows), rows)
	}
	if !strings.HasSuffix(rows[0], amount) {
		t.Errorf("row %q does not end with amount %q", rows[0], amount)
	}
	if utf8.RuneCountInString(rows[0]) != Width {
		t.Errorf("row %q is %d visible columns wide, want %d", rows[0], utf8.RuneCountInString(rows[0]), Width)
	}
}

func TestAsciiCharset(t *testing.T) {
	d := Doc{StoreName: "Café Zürich", Charset: "ascii",
		Totals: []KV{{Label: "TOTAL", Amount: "£1.00", Strong: true}}}
	out := string(Render(d))
	if !strings.Contains(out, "Cafe Zurich") {
		t.Errorf("diacritics should fold to ASCII, got %q", out)
	}
	if strings.Contains(out, "£") {
		t.Error("ascii mode must not emit multi-byte runes")
	}
	if !strings.Contains(out, "?1.00") {
		t.Error("unmappable runes should become ?")
	}
}

func TestUTF8PassThrough(t *testing.T) {
	d := Doc{StoreName: "فروشگاه", Totals: []KV{{Label: "جمع", Amount: "۱۲۰"}}}
	if !bytes.Contains(Render(d), []byte("فروشگاه")) {
		t.Error("utf8 mode must pass raw bytes through")
	}
}

func TestNewTransport(t *testing.T) {
	if tr, err := NewTransport(Config{Mode: "off"}); err != nil || tr != nil {
		t.Errorf("off mode: %v %v", tr, err)
	}
	if _, err := NewTransport(Config{Mode: "network"}); err == nil {
		t.Error("network without address must error")
	}
	if _, err := NewTransport(Config{Mode: "device"}); err == nil {
		t.Error("device without path must error")
	}
	if _, err := NewTransport(Config{Mode: "bogus"}); err == nil {
		t.Error("unknown mode must error")
	}
	tr, err := NewTransport(Config{Mode: "network", Address: "10.0.0.5"})
	if err != nil || !strings.Contains(tr.Describe(), "10.0.0.5:9100") {
		t.Errorf("default port not applied: %v %v", tr, err)
	}
}

func TestNetworkTransportDelivers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	received := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64*1024)
		n, _ := conn.Read(buf)
		received <- buf[:n]
	}()

	tr, err := NewTransport(Config{Mode: "network", Address: ln.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	data := Render(sampleDoc())
	if err := tr.Print(context.Background(), data); err != nil {
		t.Fatalf("print: %v", err)
	}
	select {
	case got := <-received:
		if !bytes.HasPrefix(got, cmdInit) {
			t.Error("printer did not receive the init sequence")
		}
		if !bytes.Contains(got, []byte("Coca-Cola")) {
			t.Error("printer did not receive the sale line")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("printer never received data")
	}
}

func TestNetworkTransportUnreachableFailsFast(t *testing.T) {
	tr, err := NewTransport(Config{Mode: "network", Address: "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := tr.Print(ctx, []byte("x")); err == nil {
		t.Fatal("unreachable printer must error")
	}
	if time.Since(start) > 6*time.Second {
		t.Error("failure took too long — would stall the print goroutine")
	}
}

func TestClip(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"under max returned unchanged", "hello", 10, "hello"},
		{"exact max returned unchanged", "hello", 5, "hello"},
		{"ascii over max truncates by rune count", "hello world", 5, "hello"},
		{
			// A byte-based clip(s, 42) would cut mid-"ä" here: 41 ASCII
			// bytes + the first of ä's 2 UTF-8 bytes == 42 bytes, leaving a
			// lone lead byte. Rune-based clipping must keep ä whole instead.
			name: "multi-byte rune sitting on the cut point is kept whole, not split",
			s:    strings.Repeat("a", 41) + "ä" + "extra",
			max:  42,
			want: strings.Repeat("a", 41) + "ä",
		},
		{"non-ASCII text shorter than max returned unchanged", "فروشگاه", 42, "فروشگاه"},
		{"max zero clips to empty, not a panic", "hello", 0, ""},
		{"max negative clips to empty, not a panic", "hello", -1, ""},
		{"empty string with positive max returned unchanged", "", 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clip(tt.s, tt.max)
			if got != tt.want {
				t.Errorf("clip(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("clip(%q, %d) = %q: invalid UTF-8", tt.s, tt.max, got)
			}
		})
	}
}

func TestRenderMultiByteNameAtColumnBoundary_NoInvalidUTF8(t *testing.T) {
	// invoice.bill_to (internal/pages/invoice_page.go:202) is the concrete
	// reachable call site: it appends "<label>: <CustomerName>" onto
	// Doc.Header, clipped at Width in Render (escpos.go). Mirror that shape
	// here so the boundary-crossing multi-byte character lands in the same
	// field the real bug was found in, not just a same-function stand-in.
	line := strings.Repeat("a", Width-1) + "ü" + "trailing text past the cut"
	d := sampleDoc()
	d.Header = []string{line}
	out := Render(d)
	if !utf8.Valid(out) {
		t.Error("render output contains invalid UTF-8 after clipping a name whose cut point lands mid multi-byte character")
	}
}

// ut-docs#376: kvRow's padding must be computed from rune count, not byte
// length, or any label/amount containing a multi-byte character (£, ä,
// ar/fa/tr text) gets under-padded — the row ends up narrower than Width
// visible columns, so right-alignment drifts left of where an equivalent
// all-ASCII row would land.
func TestKvRowPadsByRuneCountNotByteCount(t *testing.T) {
	ascii := kvRow("Cola", "2.80")
	multiByte := kvRow("Cola", "£2.80") // one extra byte, same rune count as "X2.80"

	if utf8.RuneCountInString(multiByte) != utf8.RuneCountInString(ascii) {
		t.Fatalf("multi-byte row should be the same visible width as the ASCII row: got %d runes vs %d runes", utf8.RuneCountInString(multiByte), utf8.RuneCountInString(ascii))
	}
	// The amount must land flush against the right edge (Width visible
	// columns), same as the ASCII case — not one column short because the
	// pound sign's extra UTF-8 byte was counted as if it were a column.
	if got := utf8.RuneCountInString(multiByte); got < Width {
		t.Errorf("row should pad to the full %d-column width, got %d visible columns: %q", Width, got, multiByte)
	}
	if !strings.HasSuffix(multiByte, "£2.80") {
		t.Errorf("amount must be flush at the end of the row, got %q", multiByte)
	}
}

// ut-docs#376: RenderText's center() helper must center on rune count —
// a multi-byte store name was getting padded as if it were one column wider
// than it visibly is, so it drifted off true center. A name with an odd
// number of extra UTF-8 bytes is used so a byte-based calculation and a
// rune-based one land on different integer results, not the same one by
// coincidence of rounding.
func TestRenderTextCentersByRuneCountNotByteCount(t *testing.T) {
	d := sampleDoc()
	name := "Café Bar Français" // 17 runes, 19 bytes (é, ç each +1 byte)
	d.StoreName = name
	out := RenderText(d)

	wantPad := (Width - utf8.RuneCountInString(name)) / 2
	gotPad := leadingSpacesStr(t, out, name)
	if gotPad != wantPad {
		t.Errorf("store name should center on rune count: want %d leading spaces, got %d", wantPad, gotPad)
	}
}

func leadingSpacesStr(t *testing.T, out string, want string) int {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, want) {
			return len(line) - len(strings.TrimLeft(line, " "))
		}
	}
	t.Fatalf("line containing %q not found in output", want)
	return -1
}

func TestRenderLabel(t *testing.T) {
	out := RenderLabel("Coca-Cola Can 330ml", "£1.40", "5000000000011", "utf8")
	if !bytes.HasPrefix(out, cmdInit) || !bytes.HasSuffix(out, cmdFeedCut) {
		t.Error("label must init and cut")
	}
	for _, want := range [][]byte{[]byte("Coca-Cola"), []byte("£1.40"), append([]byte("{B"), []byte("5000000000011")...), cmdDoubleOn} {
		if !bytes.Contains(out, want) {
			t.Errorf("label missing %q", want)
		}
	}
}
