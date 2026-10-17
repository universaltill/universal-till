package httpx

import (
	"fmt"
	"strings"
	"testing"
)

func TestBarcodeSVG(t *testing.T) {
	svg := string(BarcodeSVG("000000037"))
	if !strings.Contains(svg, "<svg") || !strings.Contains(svg, "rect") {
		t.Fatal("no svg produced for a valid receipt number")
	}
	if !strings.Contains(svg, `aria-label="000000037"`) {
		t.Error("aria label missing")
	}
	// (start* + 9 digits + stop*) × 5 bars each = 55 bar rects + 1 bg rect.
	if got := strings.Count(svg, "<rect"); got != 56 {
		t.Errorf("rect count = %d, want 56", got)
	}
	if BarcodeSVG("~") != "" || BarcodeSVG("abc") != "" || BarcodeSVG("") != "" {
		t.Error("unencodable/empty input must render nothing")
	}
	// Invoice numbers carry letters and a dash (review find: these
	// rendered BLANK when only digits were mapped).
	inv := string(BarcodeSVG("T2-INV-000001"))
	if !strings.Contains(inv, "<svg") || !strings.Contains(inv, `aria-label="T2-INV-000001"`) {
		t.Fatal("invoice display number did not render")
	}
}

// Golden module strings (1 = dark module) from an independent encoder,
// python-barcode 0.16.1's Code128, for inputs where its charset choices
// match ours (pure set B; set C for a leading, trailing or whole digit
// run). Pinning them catches a wrong pattern-table entry, checksum or
// set switch, which a self-consistency test would not (ut-docs#4248).
var code128Golden = map[string]string{
	"GV-7K3P-9QXA": "11010010000110100010001110101100010011011100111011011101011000111011001011100111011101101001101110011100101100110100011101110001011010100011000111101010001100011101011",
	"abc-123_xyz":  "110100100001001011000010010000110100001011001001101110010011100110110011100101100101110010100110000111100100101101101111011011110110110100010001100011101011",
	"Wikipedia":    "11010010000111010001101000011010011000010010100001101001010011110010110010000100001001101000011010010010110000111100100101100011101011",
	"12345678":     "1101001110010110011100100010110001110001011011000010100100011101101100011101011",
	"ABC12345678":  "110100100001010001100010001011000100010001101011101111010110011100100010110001110001011011000010100111001100101100011101011",
	"1234abcd":     "1101001110010110011100100010110001011110111010010110000100100001101000010110010000100110101110111101100011101011",
}

func TestCode128Modules(t *testing.T) {
	for code, want := range code128Golden {
		got, ok := code128Modules(code)
		if !ok {
			t.Fatalf("%q: reported unencodable", code)
		}
		if got != want {
			t.Errorf("%q:\n got %s\nwant %s", code, got, want)
		}
	}
}

// decodeCode128 reads modules back to text the way a scanner does: split
// into 11-module symbols, look each up, check the checksum, follow the
// B/C set switches. It shares only the width table and the start/switch
// constants with the encoder.
func decodeCode128(t *testing.T, modules string) string {
	t.Helper()
	pattern := map[string]int{}
	for v, w := range code128Widths[:106] {
		var b strings.Builder
		for j, c := range w {
			bit := "1"
			if j%2 == 1 {
				bit = "0"
			}
			b.WriteString(strings.Repeat(bit, int(c-'0')))
		}
		pattern[b.String()] = v
	}
	if !strings.HasSuffix(modules, "1100011101011") || (len(modules)-13)%11 != 0 {
		t.Fatalf("no stop pattern / bad length: %s", modules)
	}
	var values []int
	for i := 0; i < len(modules)-13; i += 11 {
		v, ok := pattern[modules[i:i+11]]
		if !ok {
			t.Fatalf("unknown symbol %s at module %d", modules[i:i+11], i)
		}
		values = append(values, v)
	}
	sum := values[0]
	for i, v := range values[1 : len(values)-1] {
		sum += (i + 1) * v
	}
	if sum%103 != values[len(values)-1] {
		t.Fatalf("checksum %d, symbol says %d", sum%103, values[len(values)-1])
	}
	var out strings.Builder
	set := map[int]byte{code128StartB: 'B', code128StartC: 'C'}[values[0]]
	if set == 0 {
		t.Fatalf("bad start symbol %d", values[0])
	}
	for _, v := range values[1 : len(values)-1] {
		switch {
		case set == 'B' && v == code128SwitchC:
			set = 'C'
		case set == 'C' && v == code128SwitchB:
			set = 'B'
		case set == 'B' && v < 95:
			out.WriteByte(byte(v + 0x20))
		case set == 'C' && v < 100:
			fmt.Fprintf(&out, "%02d", v)
		default:
			t.Fatalf("symbol %d not valid in set %c", v, set)
		}
	}
	return out.String()
}

func TestCode128RoundTrip(t *testing.T) {
	for _, code := range []string{
		"7712345678901",                        // odd all-digit card number
		"ab1234cd",                             // 4-digit run mid-code stays in B
		"ab123456cd",                           // 6-digit run mid-code goes to C
		"GV-0000123",                           // odd trailing run
		"0b6f5f6e-3c1a-4f7e-9d2b-5a8c1e4d7f90", // uuid voucher id
		"1", "12", "123", "1234", "x",
		` !"#$%&'()*+,-./:;<=>?@[\]^_` + "`{|}~",
		strings.Repeat("9", 60),
	} {
		m, ok := code128Modules(code)
		if !ok {
			t.Fatalf("%q: reported unencodable", code)
		}
		if got := decodeCode128(t, m); got != code {
			t.Errorf("round trip %q -> %q", code, got)
		}
	}
	// Set C is what keeps a numeric card number narrow enough to scan.
	digits, _ := code128Modules("7712345678901")
	// start B, "7", switch C, 6 digit pairs, checksum = 10 symbols + stop;
	// set B alone would need 15 symbols + stop.
	if len(digits) != 10*11+13 {
		t.Errorf("13-digit code used %d modules, want %d", len(digits), 10*11+13)
	}
}

// modulesFromSVG reads the dark modules back out of a rendered SVG (one
// viewBox unit per module) so the test checks what a scanner would see,
// not just the module string.
func modulesFromSVG(t *testing.T, svg string) string {
	t.Helper()
	var width int
	if _, err := fmt.Sscanf(svg[strings.Index(svg, `viewBox="0 0 `)+len(`viewBox="0 0 `):], "%d", &width); err != nil {
		t.Fatalf("viewBox: %v", err)
	}
	dark := []byte(strings.Repeat("0", width))
	g := svg[strings.Index(svg, `<g fill="#000">`):]
	for _, part := range strings.Split(g, "<rect ")[1:] {
		var x, w int
		if _, err := fmt.Sscanf(part, `x="%d" y="0" width="%d"`, &x, &w); err != nil {
			t.Fatalf("rect %q: %v", part, err)
		}
		for m := x; m < x+w; m++ {
			dark[m] = '1'
		}
	}
	return string(dark)
}

func TestCode128SVG(t *testing.T) {
	for code, want := range code128Golden {
		svg := string(Code128SVG(code))
		if !strings.Contains(svg, `class="receipt-barcode"`) {
			t.Fatalf("%q: no receipt-barcode svg: %s", code, svg)
		}
		quiet := strings.Repeat("0", code128Quiet)
		if got := modulesFromSVG(t, svg); got != quiet+want+quiet {
			t.Errorf("%q: svg modules\n got %s\nwant %s", code, got, quiet+want+quiet)
		}
	}
	// 2px modules while the symbol fits the receipt column; 1px beyond it
	// rather than a fractional CSS scale.
	if svg := string(Code128SVG("GV-7K3P-9QXA")); !strings.Contains(svg, `viewBox="0 0 187 44" width="374"`) {
		t.Errorf("short code should render at 2px modules: %s", svg[:200])
	}
	if svg := string(Code128SVG("gc-2026-abcdefghijkl")); !strings.Contains(svg, `viewBox="0 0 275 44" width="275"`) {
		t.Errorf("wide code should render at 1px modules: %s", svg[:200])
	}
	// Lowercase and longer-than-CODE39 codes are exactly the ones this exists for.
	long := strings.Repeat("ab", 30) // 60 chars — the printed receipt's ceiling
	if Code128SVG(long) == "" {
		t.Error("60-char code must render")
	}
	if Code128SVG(long+"c") != "" {
		t.Error("61-char code must render nothing (printed receipt skips it too)")
	}
	uuidCode := "0b6f5f6e-3c1a-4f7e-9d2b-5a8c1e4d7f90" // blank-code API fallback
	if Code128SVG(uuidCode) == "" {
		t.Error("uuid voucher id must render")
	}
	for _, bad := range []string{"", "café", "tab\there", "\x7f"} {
		if Code128SVG(bad) != "" {
			t.Errorf("%q: unencodable input must render nothing", bad)
		}
	}
	// The aria label is escaped: a code is operator input.
	if svg := string(Code128SVG(`a"<b>&`)); !strings.Contains(svg, `aria-label="a&#34;&lt;b&gt;&amp;"`) {
		t.Errorf("aria label not escaped: %s", svg)
	}
}
