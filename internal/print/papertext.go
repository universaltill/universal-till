package print

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// paperControl reports runes a printer could act on, or that rearrange a
// printed line (ut-docs#5847): Unicode Cc (C0 U+0000-001F, DEL U+007F, C1
// U+0080-009F), the line/paragraph separators U+2028/U+2029 and the bidi
// embeddings/overrides U+202A-U+202E. Bidi marks (LRM/RLM/ALM), isolates
// U+2066-2069 and ZWNJ/ZWJ are NOT listed: the ar/fa labels carry them.
func paperControl(r rune) bool {
	return unicode.IsControl(r) ||
		r == ' ' || r == ' ' || (r >= '‪' && r <= '‮')
}

// paperText makes free text safe to print: each paperControl rune becomes one
// ASCII space, each invalid UTF-8 byte one '?', everything else is kept
// byte-for-byte. displayWidth is unchanged (a Cc rune and an invalid byte
// already count one column each), so alignment holds. It returns s itself,
// without allocating, when nothing needs replacing.
func paperText(s string) string {
	clean := true
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || paperControl(r) {
			clean = false
			break
		}
		i += size
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteByte('?')
		case paperControl(r):
			b.WriteByte(' ')
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

func paperTexts(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = paperText(s)
	}
	return out
}

func paperKVs(in []KV) []KV {
	if in == nil {
		return nil
	}
	out := make([]KV, len(in))
	for i, kv := range in {
		kv.Label, kv.Amount = paperText(kv.Label), paperText(kv.Amount)
		out[i] = kv
	}
	return out
}

// paperVoucher returns a sanitised copy of v.
func paperVoucher(v VoucherBlock) VoucherBlock {
	v.Label, v.Amount, v.Holder = paperText(v.Label), paperText(v.Amount), paperText(v.Holder)
	v.Balance, v.Code, v.Void = paperText(v.Balance), paperText(v.Code), paperText(v.Void)
	return v
}

// paperDoc returns a copy of d with every text field sanitised, in freshly
// allocated slices: d's slices alias the caller's arrays, which must not be
// mutated. Raster fields and settings are copied unchanged. Used by the
// plain-text renderers; the ESC/POS ones sanitise in encodeText.
func paperDoc(d Doc) Doc {
	d.StoreName = paperText(d.StoreName)
	d.Header = paperTexts(d.Header)
	d.Meta = paperTexts(d.Meta)
	if d.Lines != nil {
		lines := make([]Line, len(d.Lines))
		for i, l := range d.Lines {
			l.Name, l.Qty, l.Amount, l.Sub = paperText(l.Name), paperText(l.Qty), paperText(l.Amount), paperText(l.Sub)
			lines[i] = l
		}
		d.Lines = lines
	}
	d.Totals = paperKVs(d.Totals)
	d.Payments = paperKVs(d.Payments)
	d.Footer = paperTexts(d.Footer)
	d.Barcode = paperText(d.Barcode)
	d.TrackingCaption = paperText(d.TrackingCaption)
	d.TrackingURL = paperText(d.TrackingURL)
	d.VoucherHeading = paperText(d.VoucherHeading)
	if d.Vouchers != nil {
		vs := make([]VoucherBlock, len(d.Vouchers))
		for i, v := range d.Vouchers {
			vs[i] = paperVoucher(v)
		}
		d.Vouchers = vs
	}
	return d
}

// paperSlip returns a sanitised copy of s (plain-text voucher slip).
func paperSlip(s VoucherSlip) VoucherSlip {
	s.StoreName = paperText(s.StoreName)
	s.Header = paperTexts(s.Header)
	s.Note = paperText(s.Note)
	s.Date = paperText(s.Date)
	s.Text = paperTexts(s.Text)
	s.Voucher = paperVoucher(s.Voucher)
	return s
}
