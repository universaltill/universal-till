package print

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
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
	// GS k 73 <len> <data> — CODE128 of the receipt no., sets B/C chosen for
	// the fewest symbols (ut-docs#4403).
	data, _ := code128Data("000000042")
	want := append([]byte{0x1d, 0x6b, 0x49, byte(len(data))}, data...)
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

// ut-docs#1245: the §6 KassenSichV TSE evidence QR prints as a centered
// GS v 0 raster between the barcode block and the footer. Same pattern as
// TestRenderIncludesLogo — the raster is pre-encoded by RasterLogo, so this
// layer only places the bytes.
func TestRenderIncludesTSEQR(t *testing.T) {
	qr := RasterLogo(pngBytes(t, 16, 8))
	if qr == nil {
		t.Fatal("test raster failed to encode")
	}
	d := sampleDoc()
	d.Barcode = "000000042"
	d.TSEQR = qr
	out := Render(d)

	qrIdx := bytes.Index(out, qr)
	if qrIdx < 0 {
		t.Fatal("rendered document does not contain the TSE QR raster")
	}
	// Centered: the raster bytes are written directly after ESC a 1.
	if qrIdx < len(cmdAlignMid) || !bytes.Equal(out[qrIdx-len(cmdAlignMid):qrIdx], cmdAlignMid) {
		t.Error("TSE QR raster is not preceded by align-centre")
	}
	// Placed after the barcode block…
	bcIdx := bytes.Index(out, cmdBarcodeCode128)
	if bcIdx < 0 {
		t.Fatal("barcode block missing from sample render")
	}
	if qrIdx < bcIdx {
		t.Error("TSE QR must print after the barcode block")
	}
	// …and before the footer.
	ftIdx := bytes.Index(out, []byte("Thank you!"))
	if ftIdx < 0 {
		t.Fatal("footer missing from sample render")
	}
	if qrIdx > ftIdx {
		t.Error("TSE QR must print before the footer")
	}
}

func TestRenderNoTSEQRBlockWhenUnset(t *testing.T) {
	// sampleDoc has neither logo nor TSE QR, so no GS v 0 raster header may
	// appear anywhere in the stream — nil/empty TSEQR is a no-op.
	out := Render(sampleDoc())
	if bytes.Contains(out, []byte{0x1d, 0x76, 0x30, 0x00}) {
		t.Error("GS v 0 raster block present with no logo and no TSE QR")
	}
}

// ut-docs#1245 review (B2): RenderText backs two real callers, not just the
// receipt-designer preview — system.go's PrintDoc pipes this exact string to
// `lp` for a "system"-mode (CUPS/office) printer, which cannot render a
// raster at all. An earlier version of this change printed a fixed
// "[TSE QR]" placeholder there, which meant a real customer's TSE-signed
// receipt would print that literal placeholder text in place of the actual
// evidence on that printer type — worse than showing nothing, and exactly
// the "never placeholders" policy this codebase already applies to the TSE
// text lines (print_api.go). TSEQR must therefore have NO representation in
// RenderText at all, matching Logo (the other raster-only Doc field, which
// RenderText also never touches) — the evidence itself isn't lost on this
// path, since doc.Meta's text lines (serial/transaction/counter/algorithm/
// signature) print unchanged; only the scannable form is thermal-only.
func TestRenderTextIgnoresTSEQR(t *testing.T) {
	d := sampleDoc()
	d.Barcode = "000000042"
	d.TSEQR = RasterLogo(pngBytes(t, 16, 8))
	out := RenderText(d)
	if strings.Contains(out, "TSE QR") {
		t.Error("RenderText must not represent TSEQR in any form — it cannot honestly render a raster as plain text (ut-docs#1245 review B2)")
	}
	// The rest of the preview must still render normally around it.
	if !strings.Contains(out, "║█║") || !strings.Contains(out, "000000042") {
		t.Fatal("barcode preview broken by the TSEQR change")
	}
}

func TestRenderKickDrawer(t *testing.T) {
	d := sampleDoc()
	d.KickDrawer = true
	if !bytes.Contains(Render(d), cmdKickDrawer) {
		t.Error("drawer kick missing")
	}
}

// TestRenderKickDrawerPin covers ut-docs#1136: the drawer-kick connector pin
// is a setting now, not a fixed constant. DrawerPin's zero value (an
// unset/never-migrated Doc, same as every existing caller/test before this
// setting existed) must still emit pin 2 -- that's the "preserves today's
// behaviour for every existing install" requirement, not just pin 5's own
// default.
func TestRenderKickDrawerPin(t *testing.T) {
	pin2 := []byte{0x1b, 0x70, 0x00, 0x19, 0xfa} // ESC p 0 25 250 -- connector pin 2
	pin5 := []byte{0x1b, 0x70, 0x01, 0x19, 0xfa} // ESC p 1 25 250 -- connector pin 5

	cases := []struct {
		name string
		pin  int
		want []byte
	}{
		{"zero value defaults to pin 2", 0, pin2},
		{"explicit pin 2", 2, pin2},
		{"explicit pin 5", 5, pin5},
		{"unrecognised value falls back to pin 2", 9, pin2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := sampleDoc()
			d.KickDrawer = true
			d.DrawerPin = c.pin
			out := Render(d)
			if !bytes.Contains(out, c.want) {
				t.Errorf("DrawerPin=%d: want kick bytes %x in output, not found", c.pin, c.want)
			}
			other := pin2
			if bytes.Equal(c.want, pin2) {
				other = pin5
			}
			if bytes.Contains(out, other) {
				t.Errorf("DrawerPin=%d: unwanted kick bytes %x also present", c.pin, other)
			}
		})
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

// ut-docs#1243: cp858 mode transcodes text for single-byte-codepage thermal
// printers so currency symbols print correctly instead of as mojibake. The
// two symbols that matter for the pilot markets are € (0xD5 in CP858 — the
// slot that distinguishes CP858 from CP850) and £ (0x9C).
func TestCP858EncodesCurrencySymbols(t *testing.T) {
	if got, want := encodeText("€2.50", "cp858"), []byte{0xd5, '2', '.', '5', '0'}; !bytes.Equal(got, want) {
		t.Errorf("euro: encodeText = %x, want %x", got, want)
	}
	if got, want := encodeText("£1.00", "cp858"), []byte{0x9c, '1', '.', '0', '0'}; !bytes.Equal(got, want) {
		t.Errorf("pound: encodeText = %x, want %x", got, want)
	}
}

// ut-docs#1243: a rune CP858 cannot represent (Farsi/Arabic text, if cp858
// mode is ever selected on such a till) must fall back to '?' per rune —
// mirroring the ascii branch — never error out or corrupt the byte stream.
func TestCP858UnmappableRuneFallsBackToQuestionMark(t *testing.T) {
	got := encodeText("جمع", "cp858")
	if want := []byte{'?', '?', '?'}; !bytes.Equal(got, want) {
		t.Errorf("encodeText = %x, want %x (one '?' per unmappable rune)", got, want)
	}
}

// ut-docs#1243: a cp858 document must select the printer's PC858 code page
// (ESC t 19) exactly once, immediately after init — and the utf8/ascii/empty
// modes must keep today's byte streams, which never touch code-page state.
func TestRenderCP858EmitsCodepageSelect(t *testing.T) {
	sel := []byte{0x1b, 0x74, 0x13} // ESC t 19 — PC858

	d := sampleDoc()
	d.Charset = "cp858"
	if out := Render(d); !bytes.HasPrefix(out, append(append([]byte{}, cmdInit...), sel...)) {
		t.Error("cp858 render must emit ESC t 19 immediately after init")
	}
	for _, cs := range []string{"", "utf8", "ascii"} {
		d.Charset = cs
		if bytes.Contains(Render(d), sel) {
			t.Errorf("charset %q must not emit a code-page select", cs)
		}
	}
}

func TestRenderKitchenTicketCP858EmitsCodepageSelect(t *testing.T) {
	sel := []byte{0x1b, 0x74, 0x13}
	tk := KitchenTicket{OrderNo: "42", Items: []KitchenItem{{Qty: "1", Name: "Tea"}}}

	tk.Charset = "cp858"
	if out := RenderKitchenTicket(tk); !bytes.HasPrefix(out, append(append([]byte{}, cmdInit...), sel...)) {
		t.Error("cp858 kitchen ticket must emit ESC t 19 immediately after init")
	}
	for _, cs := range []string{"", "utf8", "ascii"} {
		tk.Charset = cs
		if bytes.Contains(RenderKitchenTicket(tk), sel) {
			t.Errorf("charset %q must not emit a code-page select", cs)
		}
	}
}

func TestRenderLabelCP858EmitsCodepageSelect(t *testing.T) {
	sel := []byte{0x1b, 0x74, 0x13}

	out := RenderLabelWithUnitPrice("Coca-Cola Can 330ml", "€1.40", "", "5000000000011", "cp858")
	if !bytes.HasPrefix(out, append(append([]byte{}, cmdInit...), sel...)) {
		t.Error("cp858 label must emit ESC t 19 immediately after init")
	}
	if !bytes.Contains(out, []byte{0xd5, '1', '.', '4', '0'}) {
		t.Error("cp858 label price must carry the CP858 euro byte 0xD5")
	}
	for _, cs := range []string{"", "utf8", "ascii"} {
		if bytes.Contains(RenderLabelWithUnitPrice("X", "1.40", "", "123", cs), sel) {
			t.Errorf("charset %q must not emit a code-page select", cs)
		}
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

func labelBarcodeData(code string) []byte {
	data, _ := code128Data(code)
	return data
}

func TestRenderLabel(t *testing.T) {
	out := RenderLabelWithUnitPrice("Coca-Cola Can 330ml", "£1.40", "", "5000000000011", "utf8")
	if !bytes.HasPrefix(out, cmdInit) || !bytes.HasSuffix(out, cmdFeedCut) {
		t.Error("label must init and cut")
	}
	for _, want := range [][]byte{[]byte("Coca-Cola"), []byte("£1.40"), labelBarcodeData("5000000000011"), cmdDoubleOn} {
		if !bytes.Contains(out, want) {
			t.Errorf("label missing %q", want)
		}
	}
}

// ut-docs#3391: a pre-packed item's unit price prints on its own normal-
// size line between the (double-size) pack price and the barcode; an empty
// unit price prints nothing extra -- the barcode follows the price directly.
func TestRenderLabelWithUnitPrice(t *testing.T) {
	plain := RenderLabelWithUnitPrice("Rice 300g", "£2.00", "", "5000000000011", "utf8")
	afterPrice := append([]byte("£2.00\n"), cmdDoubleOff...)
	idx := bytes.Index(plain, afterPrice)
	if idx < 0 {
		t.Fatalf("plain label must still print the bare price, got %q", plain)
	}
	if rest := plain[idx+len(afterPrice):]; !bytes.HasPrefix(rest, []byte{0x1d}) {
		t.Fatalf("empty unit price must print nothing between the price and the barcode's own commands, got %q", rest)
	}
	out := RenderLabelWithUnitPrice("Rice 300g", "£2.00", "£6.67 per kg", "5000000000011", "utf8")
	want := []byte("£2.00\n")
	want = append(want, cmdDoubleOff...)
	want = append(want, []byte("£6.67 per kg\n")...)
	if !bytes.Contains(out, want) {
		t.Fatalf("unit price must follow the pack price at normal size, got %q", out)
	}
	if bytes.Index(out, []byte("£6.67 per kg")) > bytes.Index(out, []byte("{B")) {
		t.Fatal("unit price must print above the barcode")
	}
}

// ut-docs#2880 review: Meta lines were clipped to Width, so a TSE signature
// (88 base64 chars) or serial (64 hex chars) lost its tail on paper, as would
// a signer's 200-char receipt line. A long Meta line must wrap, never lose
// characters, on both the ESC/POS and the plain-text renderings.
func TestRenderWrapsLongMetaLines(t *testing.T) {
	d := sampleDoc()
	sig := "TSE signature: kHCYf4/f/zz+51m5XlzLtuEOFvVyAF3wrzsz+p+iyQcxzICkza8O9m/P45pRDQRWwEvxYzYZQWR3wGqEsSf2iA=="
	d.Meta = append(d.Meta, sig)
	for name, out := range map[string]string{"Render": string(Render(d)), "RenderText": RenderText(d)} {
		joined := strings.NewReplacer("\n", "", "\r", "").Replace(out)
		if !strings.Contains(joined, sig) {
			t.Errorf("%s: long Meta line lost characters (want it wrapped in full)", name)
		}
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "kHCY") && utf8.RuneCountInString(l) > Width {
				t.Errorf("%s: wrapped line wider than %d: %q", name, Width, l)
			}
		}
	}
}

// ut-docs#3337: Line.Sub prints as an extra indented row under the
// Name/Amount row, wrapped (never clipped) to the column width; an empty
// Sub renders byte-identical to a Line that never had the field.
func TestLayoutLineSubRow(t *testing.T) {
	base := Line{Name: "Wine", Qty: "3", Amount: "£36.00"}
	withSub := base
	withSub.Sub = "Unit price (excl. VAT): £10.00 - VAT 20.00%"

	plain := layoutLine(base)
	rows := layoutLine(withSub)
	if len(rows) <= len(plain) {
		t.Fatalf("expected Sub to add row(s), got %q", rows)
	}
	for i := range plain {
		if rows[i] != plain[i] {
			t.Fatalf("Sub must not change the Name/Amount row: got %q, want %q", rows[i], plain[i])
		}
	}
	var parts []string
	for _, r := range rows[len(plain):] {
		if !strings.HasPrefix(r, "  ") {
			t.Fatalf("Sub row must be indented, got %q", r)
		}
		if utf8.RuneCountInString(r) > Width {
			t.Fatalf("Sub row exceeds Width: %q", r)
		}
		parts = append(parts, strings.TrimPrefix(r, "  "))
	}
	if got := strings.Join(parts, " "); got != withSub.Sub {
		t.Fatalf("Sub content lost across wrapped rows: got %q, want %q", got, withSub.Sub)
	}
	// Breaks at a space: the rate stays whole on one row.
	if !strings.HasSuffix(rows[len(rows)-1], "20.00%") || strings.Contains(strings.Join(rows, "\n"), "20.\n") {
		t.Fatalf("expected a word wrap keeping \"20.00%%\" whole, got %q", rows)
	}
}

func TestWrapWords(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want []string
	}{
		{"short", 10, []string{"short"}},
		{"aa bb cc", 5, []string{"aa bb", "cc"}},
		{"abcdefghij k", 4, []string{"abcd", "efgh", "ij k"}},
	}
	for _, c := range cases {
		got := wrapWords(c.in, c.max)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("wrapWords(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestRenderEmptySubIsByteIdentical(t *testing.T) {
	d := sampleDoc()
	before := Render(d)
	for i := range d.Lines {
		d.Lines[i].Sub = ""
	}
	if !bytes.Equal(before, Render(d)) {
		t.Fatal("an empty Sub must not change the rendered bytes")
	}
	d.Lines[0].Sub = "Unit price (excl. VAT): £1.40"
	if !bytes.Contains(Render(d), []byte("\n  Unit price (excl. VAT): £1.40\n")) {
		t.Fatal("expected the Sub row in the rendered stream")
	}
	if !strings.Contains(RenderText(d), "  Unit price (excl. VAT): £1.40") {
		t.Fatal("expected the Sub row in the text rendering too")
	}
}

// ut-docs#908: a counter receipt's customer order-tracking QR prints as a
// centred caption line plus GS v 0 raster AFTER the TSE/fiscal QR and before
// the footer, then returns to left alignment.
func TestRenderIncludesTrackingQR(t *testing.T) {
	tse := RasterLogo(pngBytes(t, 16, 8))
	qr := RasterLogo(pngBytes(t, 24, 8))
	if tse == nil || qr == nil {
		t.Fatal("test raster failed to encode")
	}
	d := sampleDoc()
	d.TSEQR = tse
	d.TrackingQR = qr
	d.TrackingCaption = "Scan to track your order"
	out := Render(d)

	qrIdx := bytes.Index(out, qr)
	if qrIdx < 0 {
		t.Fatal("rendered document does not contain the tracking QR raster")
	}
	capIdx := bytes.Index(out, []byte("Scan to track your order\n"))
	if capIdx < 0 || capIdx > qrIdx {
		t.Fatal("caption line must print right above the tracking QR raster")
	}
	// Centred: align-centre precedes the caption.
	if capIdx < len(cmdAlignMid) || !bytes.Equal(out[capIdx-len(cmdAlignMid):capIdx], cmdAlignMid) {
		t.Error("tracking caption is not preceded by align-centre")
	}
	// Raster, newline, back to left alignment.
	after := out[qrIdx+len(qr):]
	if !bytes.HasPrefix(after, append([]byte{'\n'}, cmdAlignLeft...)) {
		t.Error("tracking QR raster must be followed by a newline and align-left")
	}
	if tseIdx := bytes.Index(out, tse); tseIdx < 0 || tseIdx > capIdx {
		t.Error("tracking QR block must print after the TSE QR")
	}
	if ftIdx := bytes.Index(out, []byte("Thank you!")); ftIdx < qrIdx {
		t.Error("tracking QR must print before the footer")
	}
}

// A caption longer than the paper is clipped to Width runes, never wrapped
// into the raster.
func TestRenderTrackingCaptionClipped(t *testing.T) {
	d := sampleDoc()
	d.TrackingQR = RasterLogo(pngBytes(t, 16, 8))
	d.TrackingCaption = strings.Repeat("x", Width+10)
	out := Render(d)
	if !bytes.Contains(out, append(append([]byte{}, cmdAlignMid...), []byte(strings.Repeat("x", Width)+"\n")...)) ||
		bytes.Contains(out, []byte(strings.Repeat("x", Width+1))) {
		t.Error("caption must be clipped to Width")
	}
}

// Empty tracking fields leave both renderings byte-identical to the
// pre-ut-docs#908 output (hashes pinned from the build before the fields
// existed: sampleDoc + barcode + a 16x8 TSE raster).
func TestRenderEmptyTrackingIsByteIdentical(t *testing.T) {
	d := sampleDoc()
	d.Barcode = "000000042"
	d.TSEQR = RasterLogo(pngBytes(t, 16, 8))
	if got := fmt.Sprintf("%x", sha256.Sum256(Render(d))); got != "a31173c3febee2dc00c28e0106d30d3df7b435bf386bef4a54c7acdea27a5f70" { // ut-docs#4403 re-pinned (barcode now set C)
		t.Errorf("Render changed with no tracking QR: sha256 %s", got)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(RenderText(d)))); got != "91d5d83e010994170afc2a88a44e82168558e04eb89820583110b12970e12bcf" {
		t.Errorf("RenderText changed with no tracking QR: sha256 %s", got)
	}
	// A caption alone (no raster, no URL) prints nothing either.
	d.TrackingCaption = "Scan to track your order"
	if strings.Contains(string(Render(d)), "Scan to track") || strings.Contains(RenderText(d), "Scan to track") {
		t.Error("a caption without a QR/URL must not print")
	}
}

// RenderText (designer preview AND the plain-text system-printer path) cannot
// show a raster, so it prints the caption plus the tracking URL itself,
// centred and wrapped to Width — a customer with a plain-text receipt can
// still type the link. Never a "[QR]" placeholder (ut-docs#1245 B2).
func TestRenderTextTrackingShowsCaptionAndURL(t *testing.T) {
	d := sampleDoc()
	d.TrackingQR = RasterLogo(pngBytes(t, 16, 8))
	d.TrackingCaption = "Scan to track your order"
	d.TrackingURL = "http://192.168.1.20:8080/o/0123456789abcdef0123456789abcdef?lang=en"
	out := RenderText(d)
	if !strings.Contains(out, "Scan to track your order") {
		t.Fatal("caption missing from the text rendering")
	}
	joined := strings.ReplaceAll(strings.ReplaceAll(out, "\n", ""), " ", "")
	if !strings.Contains(joined, d.TrackingURL) {
		t.Fatalf("tracking URL missing from the text rendering:\n%s", out)
	}
	for _, row := range strings.Split(out, "\n") {
		if utf8.RuneCountInString(row) > Width {
			t.Errorf("row wider than Width: %q", row)
		}
	}
	if strings.Contains(out, "[QR]") {
		t.Error("no placeholder for the raster")
	}
	if strings.Index(out, "Scan to track") > strings.Index(out, "Thank you!") {
		t.Error("tracking block must precede the footer")
	}
}

// ut-docs#4224: every issued voucher prints its value, holder and code,
// the code also as a CODE128 symbol so scanning the receipt redeems it.
func TestRenderVoucherBlock(t *testing.T) {
	d := sampleDoc()
	d.VoucherHeading = "Vouchers issued"
	d.Vouchers = []VoucherBlock{
		{Label: "Gift voucher", Amount: "€25.00", Holder: "For: Anna", Code: "GV-12345"},
		{Label: "Gift voucher", Amount: "€10.00", Code: "GV-VOID1", Void: "VOID - not valid"},
	}
	out := Render(d)
	for _, want := range []string{"Vouchers issued", "For: Anna", "€25.00", "VOID - not valid", "GV-VOID1"} {
		if !bytes.Contains(out, encodeText(want, d.Charset)) {
			t.Errorf("printed receipt lacks %q", want)
		}
	}
	sym := func(code string) []byte {
		data, _ := code128Data(code)
		return append([]byte{0x1d, 0x6b, 0x49, byte(len(data))}, data...)
	}
	if !bytes.Contains(out, sym("GV-12345")) {
		t.Error("active voucher code not printed as CODE128")
	}
	if bytes.Contains(out, sym("GV-VOID1")) {
		t.Error("void voucher must never print as a scannable barcode")
	}
	// The block sits after the payments and before the footer.
	if i, j := bytes.Index(out, []byte("Change")), bytes.Index(out, []byte("Vouchers issued")); i < 0 || j < i {
		t.Error("voucher block must follow the payments")
	}
	if i, j := bytes.Index(out, []byte("Vouchers issued")), bytes.Index(out, []byte("Thank you!")); j < i {
		t.Error("voucher block must precede the footer")
	}

	// A code the symbol can't carry keeps its text, without the symbol.
	d.Vouchers = []VoucherBlock{{Label: "Gift voucher", Amount: "€5.00", Code: "Gutschein-ü"}}
	out = Render(d)
	if bytes.Contains(out, cmdBarcodeCode128) {
		t.Error("unencodable code printed a barcode")
	}
	if !bytes.Contains(out, encodeText("Gutschein-ü", d.Charset)) {
		t.Error("unencodable code lost its text")
	}

	// No vouchers: byte-identical to a doc without the fields.
	noVouchers := sampleDoc()
	noVouchers.VoucherHeading = "Vouchers issued"
	if !bytes.Equal(Render(sampleDoc()), Render(noVouchers)) {
		t.Error("a heading with no vouchers changed the receipt")
	}
}

func TestRenderTextVoucherBlock(t *testing.T) {
	d := sampleDoc()
	d.VoucherHeading = "Vouchers issued"
	d.Vouchers = []VoucherBlock{{Label: "Gift voucher", Amount: "€25.00", Holder: "For: Anna", Code: "GV-12345"}}
	out := RenderText(d)
	for _, want := range []string{"Vouchers issued", "For: Anna", "€25.00", "GV-12345"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain-text receipt lacks %q", want)
		}
	}
	if strings.Contains(out, "║█║") {
		t.Error("plain-text voucher block must not print fake barcode glyphs")
	}
}

// code128Decode reverses code128Data: {B / {C switch sets, {{ is a literal
// '{' in set B, and a byte 0-99 in set C is a digit pair (ut-docs#4403).
func code128Decode(t *testing.T, data []byte) string {
	t.Helper()
	var out []byte
	set := byte(0)
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case set == 0:
			if c != '{' || i+1 >= len(data) || (data[i+1] != 'B' && data[i+1] != 'C') {
				t.Fatalf("data must start with a code-set selector, got %q", data)
			}
			set = data[i+1]
			i++
		case set == 'C' && c <= 99:
			out = append(out, '0'+c/10, '0'+c%10)
		case c == '{' && i+1 < len(data):
			i++
			switch data[i] {
			case 'B', 'C':
				set = data[i]
			case '{':
				if set != 'B' {
					t.Fatalf("{{ outside set B in %q", data)
				}
				out = append(out, '{')
			default:
				t.Fatalf("bad escape {%c in %q", data[i], data)
			}
		case set == 'B':
			out = append(out, c)
		default:
			t.Fatalf("byte %#x not valid in set %c in %q", c, set, data)
		}
	}
	return string(out)
}

func TestCode128DataRoundTrip(t *testing.T) {
	for _, code := range []string{
		"A", "1", "12", "123", "4006381333931", "000000042", "GV-12345",
		"1234abcd", "abcd1234", "ab1234cd", "ab123cd", "ab12345cd", "1ab", "12ab3",
		"123{456", "{", "{{", "a{b", "12{34", "{12", "99", "00", "0",
		"123e4567-e89b-12d3-a456-426614174000", "5000000000011",
		strings.Repeat("7", 59), strings.Repeat("z", 60), "x 1 ~",
	} {
		data, symbols := code128Data(code)
		if got := code128Decode(t, data); got != code {
			t.Errorf("%q: round-trip gave %q (data %q)", code, got, data)
		}
		if symbols < 1 || symbols > len(code) {
			t.Errorf("%q: symbols %d outside 1..%d", code, symbols, len(code))
		}
	}
}

func TestCode128DataExactBytes(t *testing.T) {
	cases := []struct {
		code    string
		want    []byte
		symbols int
	}{
		// 13 digits: pairs in set C, 8 symbols instead of 13.
		{"4006381333931", []byte{'{', 'B', '4', '{', 'C', 0, 63, 81, 33, 39, 31}, 8},
		{"000000042", []byte{'{', 'B', '0', '{', 'C', 0, 0, 0, 42}, 6},
		{"GV-12345", []byte{'{', 'B', 'G', 'V', '-', '1', '{', 'C', 23, 45}, 7},
		{"AB", []byte("{BAB"), 2},
		{"1234", []byte{'{', 'C', 12, 34}, 2},
		{"12", []byte{'{', 'C', 12}, 1},
		{"7", []byte("{B7"), 1},
		// literal '{' is escaped in set B (latent bug before ut-docs#4403).
		{"a{b", []byte("{Ba{{b"), 3},
	}
	for _, c := range cases {
		data, symbols := code128Data(c.code)
		if !bytes.Equal(data, c.want) || symbols != c.symbols {
			t.Errorf("%q: got %q/%d, want %q/%d", c.code, data, symbols, c.want, c.symbols)
		}
	}
	if _, n := code128Data("4006381333931"); n >= 13 {
		t.Errorf("all-digit code must beat set B's 13 symbols, got %d", n)
	}
}

func TestBarcodeModuleWidth(t *testing.T) {
	cases := []struct {
		name  string
		code  string
		width int // 0 = does not fit
	}{
		{"short digits", "000000042", 2},
		{"21 chars set B", "ABCDEFGHIJKLMNOPQRSTU", 2},           // 11*23+33=286 -> 572
		{"22 chars set B", "ABCDEFGHIJKLMNOPQRSTUV", 1},          // 297 -> 594 > 576
		{"25 lowercase", strings.Repeat("a", 25), 1},             // 330
		{"uuid-like", "abcdefgh-ijkl-mnop-qrst-uvwxyz012345", 1}, // 34 symbols -> 429
		{"47 chars set B", strings.Repeat("a", 47), 1},           // 11*49+33=572
		{"48 chars set B", strings.Repeat("a", 48), 0},           // 583
		{"60 chars set B", strings.Repeat("a", 60), 0},
		{"48 digits", strings.Repeat("1", 48), 1}, // 24 symbols -> 319, x2 > 576; HRI 48*12 = 576
		{"49 digits", strings.Repeat("1", 49), 0}, // bars fit at 1, but the HRI line (49*12 dots) does not
		{"empty", "", 0},
		{"non-ascii", "رسید", 0},
	}
	for _, c := range cases {
		n, ok := barcodeModuleWidth(c.code)
		if c.width == 0 {
			if ok || barcodePrintable(c.code) {
				t.Errorf("%s: must not fit, got width %d", c.name, n)
			}
			continue
		}
		if !ok || n != c.width || !barcodePrintable(c.code) {
			t.Errorf("%s: width %d ok=%v, want %d", c.name, n, ok, c.width)
		}
		var b bytes.Buffer
		barcode(&b, c.code)
		if !bytes.Contains(b.Bytes(), []byte{0x1d, 0x77, byte(c.width)}) {
			t.Errorf("%s: GS w %d missing from %q", c.name, c.width, b.Bytes())
		}
	}
}

// A printer that rejects GS w 1 (Epson TM-T88 documents 2-6) ignores it, so
// GS w 2 goes first: such a printer keeps width 2 instead of ESC @'s 3.
func TestBarcodeWidthOneSetsTwoFirst(t *testing.T) {
	var b bytes.Buffer
	barcode(&b, strings.Repeat("a", 25))
	if !bytes.Contains(b.Bytes(), []byte{0x1d, 0x77, 0x02, 0x1d, 0x77, 0x01}) {
		t.Errorf("width-1 barcode must send GS w 2 then GS w 1, got %q", b.Bytes())
	}
	b.Reset()
	barcode(&b, "000000042")
	if bytes.Contains(b.Bytes(), []byte{0x1d, 0x77, 0x01}) {
		t.Errorf("width-2 barcode must not send GS w 1, got %q", b.Bytes())
	}
}

// A label code too wide for the paper prints as text, so the label still
// carries it (ut-docs#4403 review).
func TestRenderLabelTooWideCodePrintsText(t *testing.T) {
	code := strings.Repeat("a", 50)
	out := RenderLabelWithUnitPrice("Tea", "2.50", "", code, "")
	if bytes.Contains(out, cmdBarcodeCode128) {
		t.Fatal("a code too wide for the paper must not print as a barcode")
	}
	if !bytes.Contains(out, []byte(code[:Width])) {
		t.Errorf("label must print the unprintable code as text, got %q", out)
	}
	if bytes.Contains(RenderLabelWithUnitPrice("Tea", "2.50", "", "", ""), []byte("\n\n")) {
		t.Error("an empty code must not add a blank text row")
	}
}

func TestBarcodeUnprintableWritesNothing(t *testing.T) {
	var b bytes.Buffer
	barcode(&b, strings.Repeat("a", 60))
	if b.Len() != 0 {
		t.Errorf("a code too wide for the paper must write nothing, got %q", b.Bytes())
	}
}

func TestRenderVoucherTooWideFallsBackToText(t *testing.T) {
	code := strings.Repeat("V", 60)
	d := sampleDoc()
	d.Vouchers = []VoucherBlock{{Label: "Gift voucher", Amount: "€5.00", Code: code}}
	out := Render(d)
	if bytes.Contains(out, cmdBarcodeCode128) {
		t.Error("code wider than the paper must not emit GS k")
	}
	for _, row := range wrapRunes(code, Width) {
		if !bytes.Contains(out, []byte(row+"\n")) {
			t.Errorf("voucher code text row %q missing", row)
		}
	}
	// A code that fits keeps its symbol.
	d.Vouchers[0].Code = "GV-12345"
	if !bytes.Contains(Render(d), cmdBarcodeCode128) {
		t.Error("fitting voucher code lost its barcode")
	}
}
