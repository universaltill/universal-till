package print

import (
	"bytes"
	"strings"
)

// VoucherSlip is a stand-alone hand-over slip for one gift voucher a sale
// issued (ut-docs#4247): the shop's logo/name/header and the same voucher
// block the receipt prints. Every string already formatted/translated.
type VoucherSlip struct {
	StoreName string
	Header    []string
	Logo      []byte // pre-encoded GS v 0 raster, optional
	Charset   string
	Voucher   VoucherBlock
	// Note is an optional extra line printed centred under the code, e.g.
	// "Reprinted 6 Nov 2026" on a slip printed again from the voucher's page
	// (ut-docs#5740). Empty = no line: the #4247 slip is byte-identical.
	Note string
	// Date is the slip's own date line ("Issued <date>" / "Topped up <date>",
	// ut-docs#6179), printed centred under the code, before Note. "" = no line.
	Date string
	// Text is the shop's own text (ut-docs#6179), one entry per stored line,
	// word-wrapped to Width and centred after a blank line. nil = nothing.
	Text []string
}

// RenderVoucherSlip produces the ESC/POS bytes of one slip: a complete
// document ending in feed+cut. It never kicks the drawer.
func RenderVoucherSlip(s VoucherSlip) []byte {
	var b bytes.Buffer
	line := func(t string) {
		b.Write(encodeText(t, s.Charset))
		b.WriteByte('\n')
	}
	b.Write(cmdInit)
	if cmd := codepageSelectCmd(s.Charset); cmd != nil {
		b.Write(cmd)
	}
	b.Write(cmdAlignMid)
	if len(s.Logo) > 0 {
		b.Write(s.Logo)
		b.WriteByte('\n')
	}
	b.Write(cmdDoubleOn)
	line(clip(s.StoreName, Width/2))
	b.Write(cmdDoubleOff)
	for _, h := range s.Header {
		line(clip(h, Width))
	}
	b.Write(cmdAlignLeft)
	line(strings.Repeat("-", Width))
	writeVoucherESC(&b, line, s.Voucher)
	if s.Date != "" {
		b.Write(cmdAlignMid)
		line(clip(s.Date, Width))
		b.Write(cmdAlignLeft)
	}
	if s.Note != "" {
		b.Write(cmdAlignMid)
		line(clip(s.Note, Width))
		b.Write(cmdAlignLeft)
	}
	line("")
	if len(s.Text) > 0 {
		b.Write(cmdAlignMid)
		for _, l := range s.Text {
			for _, row := range wrapWords(l, Width) {
				line(row)
			}
		}
		b.Write(cmdAlignLeft)
		line("")
	}
	line(strings.Repeat("-", Width))
	b.Write(cmdFeedCut)
	return b.Bytes()
}

// RenderVoucherSlipText is the plain-text (system/CUPS) form of a slip.
func RenderVoucherSlipText(s VoucherSlip) string {
	s = paperSlip(s) // ut-docs#5847
	var b strings.Builder
	center := func(t string) {
		t = clip(t, Width)
		pad := (Width - displayWidth(t)) / 2
		if pad < 0 {
			pad = 0
		}
		b.WriteString(strings.Repeat(" ", pad) + t + "\n")
	}
	center(s.StoreName)
	for _, h := range s.Header {
		center(h)
	}
	b.WriteString(strings.Repeat("-", Width) + "\n")
	writeVoucherText(&b, center, s.Voucher)
	if s.Date != "" {
		center(s.Date)
	}
	if s.Note != "" {
		center(s.Note)
	}
	if len(s.Text) > 0 {
		b.WriteString("\n")
		for _, l := range s.Text {
			for _, row := range wrapWords(l, Width) {
				if row == "" {
					b.WriteString("\n")
					continue
				}
				center(row)
			}
		}
	}
	b.WriteString("\n" + strings.Repeat("-", Width) + "\n")
	return b.String()
}
