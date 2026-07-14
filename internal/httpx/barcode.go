package httpx

import (
	"fmt"
	"html/template"
	"strings"
)

// code39 element patterns (9 elements per char: bar,space,… starting with a
// bar; 1 = wide, 0 = narrow). Receipt numbers only need digits + the * guard.
var code39 = map[rune]string{
	'0': "000110100", '1': "100100001", '2': "001100001", '3': "101100000",
	'4': "000110001", '5': "100110000", '6': "001110000", '7': "000100101",
	'8': "100100100", '9': "001100100", '*': "010010100",
}

const (
	barcodeNarrow = 2  // px per narrow element (2px modules scan well from screens)
	barcodeWide   = 6  // 3:1 ratio
	barcodeHeight = 44 // px
)

// BarcodeSVG renders a CODE39 barcode as inline SVG — no fonts, works
// offline, scans from the screen or a browser-printed page. Empty result
// for non-digit input (nothing to scan beats a wrong barcode).
func BarcodeSVG(code string) template.HTML {
	for _, r := range code {
		if r < '0' || r > '9' {
			return ""
		}
	}
	if code == "" || len(code) > 32 {
		return ""
	}
	full := "*" + code + "*"
	var rects strings.Builder
	x := barcodeNarrow * 5 // quiet zone
	for i, r := range full {
		pattern := code39[r]
		for j, el := range pattern {
			w := barcodeNarrow
			if el == '1' {
				w = barcodeWide
			}
			if j%2 == 0 { // even index = bar; odd = space (just advances x)
				fmt.Fprintf(&rects, `<rect x="%d" y="0" width="%d" height="%d"/>`, x, w, barcodeHeight)
			}
			x += w
		}
		if i < len(full)-1 {
			x += barcodeNarrow // inter-character gap
		}
	}
	x += barcodeNarrow * 5 // trailing quiet zone
	svg := fmt.Sprintf(
		`<svg class="receipt-barcode" role="img" aria-label="%s" viewBox="0 0 %d %d" width="%d" height="%d" xmlns="http://www.w3.org/2000/svg"><rect x="0" y="0" width="%d" height="%d" fill="#fff"/><g fill="#000">%s</g></svg>`,
		code, x, barcodeHeight, x, barcodeHeight, x, barcodeHeight, rects.String())
	return template.HTML(svg)
}
