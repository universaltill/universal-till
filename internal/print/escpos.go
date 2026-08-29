// Package print renders receipts to ESC/POS bytes and delivers them to a
// thermal printer (docs: architecture/receipt-printing.md). The document
// model is plain strings — callers format money and translate labels; this
// package stays dumb and byte-for-byte testable. Printing is never on the
// checkout critical path: callers fire it async and treat failure as a log
// line, not a sale blocker.
package print

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Width is the character width of an 80mm printer's default font.
const Width = 42

// Doc is one printable receipt.
type Doc struct {
	StoreName string
	Header    []string // address/contact lines, optional
	Meta      []string // receipt no, date, operator
	Lines     []Line
	Totals    []KV // subtotal, tax, TOTAL (last is emphasised)
	Payments  []KV
	Footer    []string
	// Barcode prints as CODE128 above the footer (receipt number — the
	// cashier scans it to open the sale for a refund). ASCII only.
	Barcode    string
	KickDrawer bool
	// DrawerPin selects the drawer-kick connector pin: 2 (default) or 5.
	// Any value other than 5 -- including the zero value, so every caller
	// that predates this setting (ut-docs#1136) keeps today's behaviour --
	// resolves to pin 2.
	DrawerPin int
	Charset   string // "utf8" (default) or "ascii"
	// Logo is a pre-encoded GS v 0 raster block (RasterLogo), printed
	// centered above the store name when present.
	Logo []byte
	// TSEQR is a pre-encoded GS v 0 raster block (RasterLogo) of the §6
	// KassenSichV TSE evidence QR (ut-docs#585/#1245), printed centered
	// after the barcode and before the footer when present.
	TSEQR []byte
}

// Line is one sale line.
type Line struct {
	Name   string
	Qty    string // pre-formatted ("2" / "0.350 kg")
	Amount string // pre-formatted money
}

// KV is a label/amount pair right-aligned on one row.
type KV struct {
	Label  string
	Amount string
	Strong bool
}

// ESC/POS command sequences (Epson dialect — the de-facto standard).
var (
	cmdInit           = []byte{0x1b, 0x40}
	cmdAlignLeft      = []byte{0x1b, 0x61, 0x00}
	cmdAlignMid       = []byte{0x1b, 0x61, 0x01}
	cmdBoldOn         = []byte{0x1b, 0x45, 0x01}
	cmdBoldOff        = []byte{0x1b, 0x45, 0x00}
	cmdDoubleOn       = []byte{0x1d, 0x21, 0x11}
	cmdDoubleOff      = []byte{0x1d, 0x21, 0x00}
	cmdFeedCut        = []byte{0x1d, 0x56, 0x42, 0x03}       // feed 3 + partial cut
	cmdKickDrawer     = []byte{0x1b, 0x70, 0x00, 0x19, 0xfa} // ESC p 0 25 250 -- connector pin 2
	cmdKickDrawerPin5 = []byte{0x1b, 0x70, 0x01, 0x19, 0xfa} // ESC p 1 25 250 -- connector pin 5

	cmdBarcodeHeight  = []byte{0x1d, 0x68, 0x50} // GS h 80 dots
	cmdBarcodeWidth   = []byte{0x1d, 0x77, 0x02} // GS w module 2
	cmdBarcodeHRI     = []byte{0x1d, 0x48, 0x02} // GS H print number below
	cmdBarcodeCode128 = []byte{0x1d, 0x6b, 0x49} // GS k 73 <len> <data>
)

// barcode emits a CODE128 symbol (code set B) for a printable-ASCII code.
func barcode(b *bytes.Buffer, code string) {
	if code == "" || len(code) > 60 {
		return
	}
	for _, r := range code {
		if r < 0x20 || r > 0x7e {
			return // unencodable — skip rather than jam the printer
		}
	}
	data := append([]byte{'{', 'B'}, []byte(code)...)
	b.Write(cmdBarcodeHeight)
	b.Write(cmdBarcodeWidth)
	b.Write(cmdBarcodeHRI)
	b.Write(cmdBarcodeCode128)
	b.WriteByte(byte(len(data)))
	b.Write(data)
	b.WriteByte('\n')
}

// Render produces the full ESC/POS byte stream for a document.
func Render(d Doc) []byte {
	var b bytes.Buffer
	enc := func(s string) []byte { return encodeText(s, d.Charset) }
	line := func(s string) {
		b.Write(enc(s))
		b.WriteByte('\n')
	}

	b.Write(cmdInit)
	if d.KickDrawer {
		if d.DrawerPin == 5 {
			b.Write(cmdKickDrawerPin5)
		} else {
			b.Write(cmdKickDrawer)
		}
	}

	b.Write(cmdAlignMid)
	if len(d.Logo) > 0 {
		b.Write(d.Logo) // pre-encoded GS v 0 raster (RasterLogo)
		b.WriteByte('\n')
	}
	b.Write(cmdDoubleOn)
	line(clip(d.StoreName, Width/2)) // double-width font halves the columns
	b.Write(cmdDoubleOff)
	for _, h := range d.Header {
		line(clip(h, Width))
	}
	b.Write(cmdAlignLeft)
	line(strings.Repeat("-", Width))
	for _, m := range d.Meta {
		line(clip(m, Width))
	}
	line(strings.Repeat("-", Width))

	for _, l := range d.Lines {
		for _, row := range layoutLine(l) {
			line(row)
		}
	}
	line(strings.Repeat("-", Width))

	for _, t := range d.Totals {
		if t.Strong {
			b.Write(cmdBoldOn)
		}
		line(kvRow(t.Label, t.Amount))
		if t.Strong {
			b.Write(cmdBoldOff)
		}
	}
	if len(d.Payments) > 0 {
		line("")
		for _, p := range d.Payments {
			line(kvRow(p.Label, p.Amount))
		}
	}

	if d.Barcode != "" {
		line("")
		b.Write(cmdAlignMid)
		barcode(&b, d.Barcode)
		b.Write(cmdAlignLeft)
	}

	if len(d.TSEQR) > 0 {
		line("")
		b.Write(cmdAlignMid)
		b.Write(d.TSEQR) // pre-encoded GS v 0 raster (RasterLogo)
		b.WriteByte('\n')
		b.Write(cmdAlignLeft)
	}

	if len(d.Footer) > 0 {
		line("")
		b.Write(cmdAlignMid)
		for _, f := range d.Footer {
			line(clip(f, Width))
		}
		b.Write(cmdAlignLeft)
	}

	b.Write(cmdFeedCut)
	return b.Bytes()
}

// layoutLine renders one sale line: "qty x name" left, amount right; long
// names wrap onto their own row with the amount on the last row. The
// one-row/two-row threshold is rune-based, not byte-based -- Width tracks
// visible columns, and kvRow (which this calls for the one-row case) pads
// by rune count too, so a byte-based threshold here was inconsistent with
// what kvRow can actually fit on one row: a multi-byte label/amount pair
// (é/ö/ü/ß, ar/fa/tr text) sitting between the rune-count and byte-count
// widths got routed to an unnecessary two-row fallback (ut-docs#438).
func layoutLine(l Line) []string {
	label := l.Name
	if l.Qty != "" && l.Qty != "1" {
		label = l.Qty + " x " + l.Name
	}
	if utf8.RuneCountInString(label)+1+utf8.RuneCountInString(l.Amount) <= Width {
		return []string{kvRow(label, l.Amount)}
	}
	return []string{clip(label, Width), kvRow("", l.Amount)}
}

// kvRow right-aligns amount against label on one fixed-width row. Padding is
// computed from rune count, not byte length — Width tracks visible columns,
// and a byte-based calculation under-pads any label/amount containing a
// multi-byte character (£, ä/ö/ü/ß, ar/fa/tr text), leaving the row one or
// more columns narrower than Width actually intends (ut-docs#376).
func kvRow(label, amount string) string {
	space := Width - utf8.RuneCountInString(label) - utf8.RuneCountInString(amount)
	if space < 1 {
		label = clip(label, Width-utf8.RuneCountInString(amount)-1)
		space = 1
	}
	return label + strings.Repeat(" ", space) + amount
}

// clip truncates s to at most max runes (characters), never bytes — Width
// tracks visible columns, and a byte-based cut can split a multi-byte UTF-8
// character (any non-ASCII locale string: ä/ö/ü/ß, ar/fa/tr text) mid-char,
// producing invalid UTF-8 on the printer. max <= 0 clips to empty rather
// than panicking — a caller computing max from column arithmetic (kvRow's
// re-clip when a row overflows) can legitimately land at or below zero.
func clip(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

// encodeText prepares a string for the printer. utf8 passes through (many
// modern thermals accept it); ascii strips diacritics and replaces anything
// unmappable with '?' so column math and cheap CP437 printers stay sane.
func encodeText(s, charset string) []byte {
	if charset != "ascii" {
		return []byte(s)
	}
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	folded, _, err := transform.String(t, s)
	if err != nil {
		folded = s
	}
	out := make([]byte, 0, len(folded))
	for _, r := range folded {
		if r < 0x20 || r > 0x7e {
			if r == '\t' {
				out = append(out, ' ')
				continue
			}
			out = append(out, '?')
			continue
		}
		out = append(out, byte(r))
	}
	return out
}

// Validate reports a friendly error for an obviously broken document.
func (d Doc) Validate() error {
	if strings.TrimSpace(d.StoreName) == "" {
		return fmt.Errorf("store name required")
	}
	return nil
}

// RenderText lays the document out exactly like Render but as plain text —
// the receipt designer's live preview shows what the printer will produce,
// with the barcode as a placeholder row.
func RenderText(d Doc) string {
	var b strings.Builder
	center := func(s string) {
		s = clip(s, Width)
		pad := (Width - utf8.RuneCountInString(s)) / 2
		if pad < 0 {
			pad = 0
		}
		b.WriteString(strings.Repeat(" ", pad) + s + "\n")
	}
	center(d.StoreName)
	for _, h := range d.Header {
		center(h)
	}
	b.WriteString(strings.Repeat("-", Width) + "\n")
	for _, m := range d.Meta {
		b.WriteString(clip(m, Width) + "\n")
	}
	b.WriteString(strings.Repeat("-", Width) + "\n")
	for _, l := range d.Lines {
		for _, row := range layoutLine(l) {
			b.WriteString(row + "\n")
		}
	}
	b.WriteString(strings.Repeat("-", Width) + "\n")
	for _, t := range d.Totals {
		b.WriteString(kvRow(t.Label, t.Amount) + "\n")
	}
	if len(d.Payments) > 0 {
		b.WriteString("\n")
		for _, p := range d.Payments {
			b.WriteString(kvRow(p.Label, p.Amount) + "\n")
		}
	}
	if d.Barcode != "" {
		b.WriteString("\n")
		center("║█║▌║█║▌║▌█║█║▌║")
		center(d.Barcode)
	}
	// TSEQR is deliberately NOT represented here (ut-docs#1245 review, B2):
	// RenderText backs two different callers — the receipt-designer preview
	// AND system.go's real plain-text CUPS/office-printer path (PrintDoc's
	// "system" mode literally pipes this string to `lp`; it cannot render a
	// raster at all, ESC/POS or otherwise). A fixed "[TSE QR]" placeholder
	// here isn't a preview stand-in on that path — it's exactly what would
	// print on a real customer's TSE-signed receipt, in place of the actual
	// evidence. This mirrors Logo, the other raster-only Doc field: Logo has
	// no RenderText representation either, precisely because there's no
	// honest way to show a raster as plain text. The TSE evidence itself
	// isn't lost on this printer type — doc.Meta's text lines (serial,
	// transaction no., counter, algorithm, signature) print exactly as they
	// did before this ticket; only the *scannable* form is thermal/raster-
	// only. This project's own placeholder policy already forbids this
	// (print_api.go: "fields the signer didn't return are skipped — never
	// placeholders").
	if len(d.Footer) > 0 {
		b.WriteString("\n")
		for _, f := range d.Footer {
			center(f)
		}
	}
	return b.String()
}

// RenderLabel produces one product/shelf label (docs: receipt-printing.md
// § label printing): name, price big, barcode, cut. Callers concatenate
// copies.
func RenderLabel(name, price, code, charset string) []byte {
	var b bytes.Buffer
	enc := func(s string) []byte { return encodeText(s, charset) }
	b.Write(cmdInit)
	b.Write(cmdAlignMid)
	b.Write(enc(clip(name, Width)))
	b.WriteByte('\n')
	b.Write(cmdDoubleOn)
	b.Write(enc(clip(price, Width/2)))
	b.WriteByte('\n')
	b.Write(cmdDoubleOff)
	barcode(&b, code)
	b.Write(cmdAlignLeft)
	b.Write(cmdFeedCut)
	return b.Bytes()
}
