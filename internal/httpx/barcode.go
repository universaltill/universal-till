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

// code128Widths holds the bar/space widths (in modules, starting with a
// bar) of CODE128 symbol values 0–106; 106 is the stop pattern.
var code128Widths = [...]string{
	"212222", "222122", "222221", "121223", "121322", "131222", "122213", "122312", "132212", "221213",
	"221312", "231212", "112232", "122132", "122231", "113222", "123122", "123221", "223211", "221132",
	"221231", "213212", "223112", "312131", "311222", "321122", "321221", "312212", "322112", "322211",
	"212123", "212321", "232121", "111323", "131123", "131321", "112313", "132113", "132311", "211313",
	"231113", "231311", "112133", "112331", "132131", "113123", "113321", "133121", "313121", "211331",
	"231131", "213113", "213311", "213131", "311123", "311321", "331121", "312113", "312311", "332111",
	"314111", "221411", "431111", "111224", "111422", "121124", "121421", "141122", "141221", "112214",
	"112412", "122114", "122411", "142112", "142211", "241211", "221114", "413111", "241112", "134111",
	"111242", "121142", "121241", "114212", "124112", "124211", "411212", "421112", "421211", "212141",
	"214121", "412121", "111143", "111341", "131141", "114113", "114311", "411113", "411311", "113141",
	"114131", "311141", "411131", "211412", "211214", "211232", "2331112",
}

const (
	code128SwitchC = 99
	code128SwitchB = 100
	code128StartB  = 104
	code128StartC  = 105
	code128Stop    = 106
	code128Quiet   = 10 // modules of quiet zone each side (the spec's minimum)
	code128MaxLen  = 60 // same ceiling as the printed receipt (internal/print barcodeEncodable)
)

// code128Values turns code into CODE128 symbol values (start code, data,
// no checksum or stop). Code set B carries any printable ASCII; a run of
// digits long enough to pay for its switch goes in code set C, two digits
// per symbol — a 13-digit card number is then ~40% narrower, which keeps
// it scannable once scaled to the receipt's width. ok is false for input
// the printed receipt can't encode either: empty, longer than 60, or
// anything outside printable ASCII.
func code128Values(code string) ([]int, bool) {
	if code == "" || len(code) > code128MaxLen {
		return nil, false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < 0x20 || code[i] > 0x7e {
			return nil, false
		}
	}
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	var values []int
	set := 0 // 'B' or 'C' once started
	use := func(want int) {
		switch {
		case set == want:
		case set == 0 && want == 'B':
			values = append(values, code128StartB)
		case set == 0:
			values = append(values, code128StartC)
		case want == 'B':
			values = append(values, code128SwitchB)
		default:
			values = append(values, code128SwitchC)
		}
		set = want
	}
	for i := 0; i < len(code); {
		j := i
		for j < len(code) && isDigit(code[j]) {
			j++
		}
		run := j - i
		// A switch costs one symbol each way: worth it for 4+ digits at an
		// end of the code, 6+ in the middle.
		atEdge := i == 0 || j == len(code)
		if run >= 6 || (run >= 4 && atEdge) {
			if run%2 == 1 { // odd: the first digit goes in set B
				use('B')
				values = append(values, int(code[i])-0x20)
				i++
			}
			use('C')
			for ; i < j; i += 2 {
				values = append(values, int(code[i]-'0')*10+int(code[i+1]-'0'))
			}
			continue
		}
		use('B')
		if run == 0 {
			j = i + 1
		}
		for ; i < j; i++ {
			values = append(values, int(code[i])-0x20)
		}
	}
	return values, true
}

// code128Modules encodes code as a CODE128 symbol and returns its modules
// (1 = bar), checksum and stop pattern included, quiet zones not.
func code128Modules(code string) (string, bool) {
	values, ok := code128Values(code)
	if !ok {
		return "", false
	}
	sum := values[0]
	for i, v := range values[1:] {
		sum += (i + 1) * v
	}
	values = append(values, sum%103, code128Stop)
	var m strings.Builder
	for _, v := range values {
		for j, w := range code128Widths[v] {
			bit := "1"
			if j%2 == 1 {
				bit = "0"
			}
			m.WriteString(strings.Repeat(bit, int(w-'0')))
		}
	}
	return m.String(), true
}

// code128FitPx is about the on-screen receipt's column width. A symbol
// wider than this at 2px modules drops to 1px modules instead of letting
// CSS scale it to a fractional width, which blurs bars a 1x till screen
// can no longer separate (ut-docs#4248: a 20-character code went from
// unreadable to scannable at 1x). That keeps modules whole up to ~28 set-B
// characters at the default column; a longer code (a 36-char uuid is
// ~450px even at 1px) is still scaled down by the receipt's max-width and
// reads only on a high-DPR screen — the printed receipt and the text stay.
const code128FitPx = 384

// Code128SVG renders a CODE128 barcode (code sets B and C) as inline SVG, for
// codes CODE39 can't carry: lowercase letters, punctuation, more than 32
// characters — a pre-printed gift card number (or a uuid voucher id; see
// code128FitPx for how far a long code stays scannable on screen)
// (ut-docs#4248). Empty result for unencodable input. preserveAspectRatio
// "none" keeps the bars full height when CSS narrows the symbol (the
// receipt pins it to 44px tall).
func Code128SVG(code string) template.HTML {
	modules, ok := code128Modules(code)
	if !ok {
		return ""
	}
	var rects strings.Builder
	for i := 0; i < len(modules); {
		if modules[i] != '1' {
			i++
			continue
		}
		j := i
		for j < len(modules) && modules[j] == '1' {
			j++
		}
		fmt.Fprintf(&rects, `<rect x="%d" y="0" width="%d" height="%d"/>`, code128Quiet+i, j-i, barcodeHeight)
		i = j
	}
	vbw := len(modules) + 2*code128Quiet // viewBox units = modules
	px := barcodeNarrow
	if vbw*px > code128FitPx {
		px = 1
	}
	return template.HTML(fmt.Sprintf(
		`<svg class="receipt-barcode" role="img" aria-label="%s" viewBox="0 0 %d %d" width="%d" height="%d" preserveAspectRatio="none" shape-rendering="crispEdges" xmlns="http://www.w3.org/2000/svg"><rect x="0" y="0" width="%d" height="%d" fill="#fff"/><g fill="#000">%s</g></svg>`,
		template.HTMLEscapeString(code), vbw, barcodeHeight, vbw*px, barcodeHeight, vbw, barcodeHeight, rects.String()))
}
