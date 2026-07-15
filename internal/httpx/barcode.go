package httpx

import (
	"fmt"
	"html/template"
	"strings"
)

// code39 element patterns (9 elements per char: bar,space,… starting with a
// bar; 1 = wide, 0 = narrow). Full CODE39 charset — invoice numbers carry
// letters and dashes (INV-000001), not just receipt digits.
var code39 = map[rune]string{
	'0': "000110100", '1': "100100001", '2': "001100001", '3': "101100000",
	'4': "000110001", '5': "100110000", '6': "001110000", '7': "000100101",
	'8': "100100100", '9': "001100100", '*': "010010100",
	'A': "100001001", 'B': "001001001", 'C': "101001000", 'D': "000011001",
	'E': "100011000", 'F': "001011000", 'G': "000001101", 'H': "100001100",
	'I': "001001100", 'J': "000011100", 'K': "100000011", 'L': "001000011",
	'M': "101000010", 'N': "000010011", 'O': "100010010", 'P': "001010010",
	'Q': "000000111", 'R': "100000110", 'S': "001000110", 'T': "000010110",
	'U': "110000001", 'V': "011000001", 'W': "111000000", 'X': "010010001",
	'Y': "110010000", 'Z': "011010000",
	'-': "010000101", '.': "110000100", ' ': "011000100",
}

const (
	barcodeNarrow = 2  // px per narrow element (2px modules scan well from screens)
	barcodeWide   = 6  // 3:1 ratio
	barcodeHeight = 44 // px
)

// BarcodeSVG renders a CODE39 barcode as inline SVG — no fonts, works
// offline, scans from the screen or a browser-printed page. Empty result
// for unencodable input (nothing to scan beats a wrong barcode).
func BarcodeSVG(code string) template.HTML {
	for _, r := range code {
		if _, ok := code39[r]; !ok || r == '*' {
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
