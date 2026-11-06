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
	line("")
	line(strings.Repeat("-", Width))
	b.Write(cmdFeedCut)
	return b.Bytes()
}

// RenderVoucherSlipText is the plain-text (system/CUPS) form of a slip.
func RenderVoucherSlipText(s VoucherSlip) string {
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
	b.WriteString("\n" + strings.Repeat("-", Width) + "\n")
	return b.String()
}
