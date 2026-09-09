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

	"golang.org/x/text/encoding/charmap"
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
	Charset   string // "utf8" (default), "ascii", "cp858", "win1250", "win1257", "win1253", or "win1254"
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
	if cmd := codepageSelectCmd(d.Charset); cmd != nil {
		b.Write(cmd) // once per document, before any text (ut-docs#1243)
	}
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
// unmappable with '?' so column math and cheap CP437 printers stay sane;
// cp858 transcodes to code page 858 (ut-docs#1243) so currency symbols
// (€ → 0xD5, £ → 0x9C) print correctly on single-byte-codepage printers,
// with the same per-rune '?' fallback as ascii for anything CP858 lacks.
func encodeText(s, charset string) []byte {
	switch charset {
	case "ascii":
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
	case "cp858":
		// Per-rune EncodeRune rather than CodePage858.NewEncoder(): the
		// encoder errors (or substitutes 0x1A) on an unmappable rune, and
		// this branch must degrade to a visible '?' per rune instead —
		// never error or corrupt the stream — matching the ascii branch.
		//
		// Two deliberate differences from the ascii branch, confirmed at
		// review (ut-docs#1243) and left as-is because cp858 is a
		// transcode, not a sanitiser:
		//   - C0/DEL control bytes (\n, \r, ESC, NUL, 0x7f) round-trip
		//     through CP858 unchanged, where ascii maps them to '?'. That
		//     matches the utf8 default, which passes them through too, so
		//     cp858 is no worse than the mode nearly every till runs; it is
		//     simply not an improvement on it.
		//   - Turkish still degrades. CP858 covers Western European letters
		//     natively (é→0x82, ü→0x81, ß→0xe1), but it has no 'ş' and —
		//     unlike CP850 — no 'ı', whose 0xD5 slot is exactly what CP858
		//     gives up to gain '€'. The fold below turns those into 's'/'i'
		//     rather than '?', but a Turkish shop still wants a Turkish code
		//     page, which is why DefaultCharset never selects cp858 for one.
		//
		// ut-docs#1728 added the fold. Until then this arm went straight to
		// '?' for anything CP858 lacks, and its own comment justified that
		// as "acceptable for an OPT-IN option labelled Western Europe".
		// Making cp858 a default removes that premise: a German or UK shop
		// that was printing fine over UTF-8 would suddenly lose everyday
		// typography — 'œ' (Bœuf), the curly quotes and en-dashes Excel and
		// Word put in imported catalogs, '…', '•' — all of which CP858
		// genuinely cannot encode (verified against charmap.CodePage858).
		// Trading a broken currency symbol for a receipt full of '?' is not
		// a fix, so unmappable runes are now folded to their closest
		// CP858-representable form first and '?' is the last resort only.
		return encodeCharmap(s, charmap.CodePage858)
	// win1250/win1257/win1253 (ut-docs#1733): same per-rune-transcode-or-fold
	// shape as cp858 above, extending currency-symbol coverage to markets
	// CP858 cannot reach without losing their own alphabet. Unlike CP858,
	// each of these already natively encodes the everyday Word/Excel
	// typography (en/em dash, curly quotes, bullet, ellipsis — verified
	// against their own EncodeRune) that charmapPunctuationFold exists to patch, so
	// they reuse the same fold table only for what's left: 'œ'/'Œ' and the
	// rest of foldToCharmap's decomposition step.
	case "win1250":
		return encodeCharmap(s, charmap.Windows1250)
	case "win1257":
		return encodeCharmap(s, charmap.Windows1257)
	case "win1253":
		return encodeCharmap(s, charmap.Windows1253)
	// win1254 (ut-docs#1775, market:tr): same per-rune-transcode-or-fold shape
	// as win1250/1257/1253 above, added later for Turkish — natively encodes
	// the Turkish alphabet plus '€'/'£'. Unlike those three, it ALSO natively
	// encodes 'œ'/'Œ' and the rest of the Word/Excel typography
	// charmapPunctuationFold exists to patch (verified against
	// charmap.Windows1254.EncodeRune — Windows-1254 is built on the same
	// Western European base as Windows-1252). That narrows what the fold
	// path is FOR here, but does not retire it: an item name carrying a
	// Central European/Baltic letter this page lacks (č/ā/ž/ą/ė/ū/Ž, …) still
	// reaches foldToCharmap's NFKD decomposition step exactly as it does for
	// win1250/1257/1253, same as ĳ/Ĳ/ŀ/Ŀ/№ still reach the explicit
	// punctuation table — win1254 is missing those too (independent review
	// finding, ut-docs#1775). Only the everyday Word/Excel typography subset
	// (dashes/quotes/bullet/ellipsis/œ/Œ) skips the fold and encodes
	// natively; '?' remains the last resort for a script this page genuinely
	// can't represent (Arabic, Farsi, CJK, …), same as every other arm here.
	case "win1254":
		return encodeCharmap(s, charmap.Windows1254)
	default: // "utf8" and the zero value — raw pass-through, unchanged
		return []byte(s)
	}
}

// Encodable reports whether s prints under charset without any rune
// degrading to the '?' last resort. Callers that carry translated text onto
// a single-byte-code-page printer (e.g. kitchen tickets, ut-docs#261/#1733)
// use this to decide whether the printer's own charset can actually render a
// locale's translation, rather than assuming any restricted charset means
// "Latin-only" — win1253 (Greek) legitimately renders Greek, for example.
//
// Checked rune-by-rune against the real encode/fold logic, NOT by scanning
// encodeText's OUTPUT bytes for '?' (independent review finding, ut-docs#1733):
// a literal '?' already present in s — a perfectly ordinary character in a
// translated string — is not a degraded rune, and a caller can never tell
// the two apart from the output alone.
func Encodable(s, charset string) bool {
	for _, r := range s {
		if !runeEncodable(r, charset) {
			return false
		}
	}
	return true
}

func runeEncodable(r rune, charset string) bool {
	switch charset {
	case "ascii":
		return r == '\t' || (r >= 0x20 && r <= 0x7e)
	case "cp858":
		return charmapRuneEncodable(r, charmap.CodePage858)
	case "win1250":
		return charmapRuneEncodable(r, charmap.Windows1250)
	case "win1257":
		return charmapRuneEncodable(r, charmap.Windows1257)
	case "win1253":
		return charmapRuneEncodable(r, charmap.Windows1253)
	case "win1254":
		return charmapRuneEncodable(r, charmap.Windows1254)
	default: // "utf8" and the zero value — raw pass-through, always encodable
		return true
	}
}

// charmapRuneEncodable mirrors encodeCharmap's own per-rune decision: r
// encodes if cp has it natively, or if foldToCharmap can fold it to
// something other than the bare '?' last resort (that single-byte 0x3F
// sentinel is the only value foldToCharmap ever returns for a rune it truly
// cannot represent — no successful punctuation substitution or NFKD
// decomposition ever collapses to it).
func charmapRuneEncodable(r rune, cp *charmap.Charmap) bool {
	if _, ok := cp.EncodeRune(r); ok {
		return true
	}
	folded := foldToCharmap(r, cp)
	return !(len(folded) == 1 && folded[0] == '?')
}

// encodeCharmap transcodes s to cp per-rune, folding anything cp cannot
// encode via foldToCharmap rather than erroring or corrupting the stream.
func encodeCharmap(s string, cp *charmap.Charmap) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if b, ok := cp.EncodeRune(r); ok {
			out = append(out, b)
			continue
		}
		out = append(out, foldToCharmap(r, cp)...)
	}
	return out
}

// codepageSelectCmd returns the ESC/POS code-page selection command for a
// charset, or nil when no selection is sent. utf8 and ascii keep today's
// behaviour of never touching the printer's code-page state. Page numbers are
// Epson's own published ESC t reference: 19 = PC858 (Euro variant of PC850),
// 45 = WPC1250, 47 = WPC1253, 48 = WPC1254, 51 = WPC1257 — the same printers
// that accept ESC t 19 also accept the Windows-125x pages, which is what
// makes ut-docs#1733's (and #1775's) fix possible without new hardware.
func codepageSelectCmd(charset string) []byte {
	switch charset {
	case "cp858":
		return []byte{0x1b, 0x74, 0x13}
	case "win1250":
		return []byte{0x1b, 0x74, 45}
	case "win1253":
		return []byte{0x1b, 0x74, 47}
	case "win1254":
		return []byte{0x1b, 0x74, 48}
	case "win1257":
		return []byte{0x1b, 0x74, 51}
	default:
		return nil
	}
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
	if cmd := codepageSelectCmd(charset); cmd != nil {
		b.Write(cmd) // once per label, before any text (ut-docs#1243)
	}
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

// charmapPunctuationFold maps typography a single-byte code page has no slot
// for onto the plain ASCII a receipt can actually print. These are the
// characters that reach a receipt through ordinary content rather than
// through an exotic alphabet: Word/Excel autocorrect puts curly quotes and
// en-dashes into imported catalogs and footer text, so a till whose shop
// name or "thank you" line came from a spreadsheet hits them on every single
// sale. Shared across cp858/win1250/win1257/win1253/win1254 (ut-docs#1733,
// #1775) — CP858 is the only one of the five that actually needs most of
// these (the Windows 125x pages already natively encode dashes/quotes/
// bullet/ellipsis), but the table costs nothing to share and 'œ'/'Œ' and the
// rest of the decomposition step in foldToCharmap are missing from all five
// alike.
var charmapPunctuationFold = map[rune]string{
	'\u2010': "-", '\u2011': "-", '\u2012': "-", '\u2013': "-", // hyphen, non-breaking hyphen, figure dash, en dash
	'\u2014': "-", '\u2015': "-", '\u2212': "-", // em dash, horizontal bar, minus sign
	'\u2018': "'", '\u2019': "'", '\u201b': "'", '\u2032': "'", // curly single quotes, prime
	'\u201a': ",",
	'\u201c': "\"", '\u201d': "\"", '\u201e': "\"", '\u201f': "\"", '\u2033': "\"",
	'\u2022': "*", '\u2023': "*", '\u2043': "-", // bullets
	'\u2026': "...",
	'\u2039': "<", '\u203a': ">",
	'\u2044': "/", '\u2116': "No.",
	// Letters with no Unicode decomposition of their own. 'œ'/'Œ' are French
	// LETTERS, not typographic ligatures like 'ﬁ', so NFKD leaves them
	// untouched and the fold below can't reach them — "Bœuf" printed "B?uf"
	// until this row existed (independent review, ut-docs#1728 finding 6).
	'\u0153': "oe", '\u0152': "OE", // œ Œ
	'\u0133': "ij", '\u0132': "IJ", // ĳ Ĳ (Dutch)
	'\u0140': "l", '\u013f': "L", // ŀ Ŀ (Catalan)
	'\u00a0': " ", '\u2007': " ", '\u2009': " ", '\u200a': " ", // no-break, figure, thin, hair space
	'\u202f': " ", '\u205f': " ",
}

// foldToCharmap renders one rune cp cannot encode as the closest thing it
// can, in three escalating steps: an explicit punctuation substitution, then
// a compatibility decomposition with combining marks stripped (which turns
// 'œ'→"oe", 'ﬁ'→"fi", and any accented letter cp lacks into its base
// letter), then '?' as the visible last resort — the same last resort this
// arm has always had, just reached far less often (ut-docs#1728, generalized
// to any single-byte code page in ut-docs#1733).
func foldToCharmap(r rune, cp *charmap.Charmap) []byte {
	if sub, ok := charmapPunctuationFold[r]; ok {
		return []byte(sub)
	}
	t := transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	folded, _, err := transform.String(t, string(r))
	if err == nil && folded != "" && folded != string(r) {
		out := make([]byte, 0, len(folded))
		ok := true
		for _, fr := range folded {
			b, enc := cp.EncodeRune(fr)
			if !enc {
				ok = false
				break
			}
			out = append(out, b)
		}
		if ok {
			return out
		}
	}
	return []byte{'?'}
}
