package barcode

import (
	"fmt"
	"strings"

	"github.com/universaltill/universal-till/internal/money"
)

// Default returns the ADR-0059 §1 default registry — the ten symbologies
// every till ships with. A new symbology is a new entry appended here, per
// this package's "data, not a new if" rule — but see Match's doc comment:
// where a new entry sits in this slice matters, it is not purely
// cosmetic. Checksum/structurally-validated plain entries are declared
// before CODE128/CODE39/INTERNAL_PLU, the three permissive catch-alls that
// would otherwise swallow every one of them (ut-docs#933 review finding
// F1 — an earlier draft declared GTIN14 after CODE128/CODE39 and a valid
// GTIN-14 scan was silently typed CODE128 instead).
func Default() *Registry {
	return &Registry{entries: []Symbology{
		{ID: "EAN13", NameKey: "barcode.symbology.EAN13", Parse: parsePlainGS1("EAN13", 13)},
		{ID: "EAN8", NameKey: "barcode.symbology.EAN8", Parse: parsePlainGS1("EAN8", 8)},
		{ID: "UPCA", NameKey: "barcode.symbology.UPCA", Parse: parsePlainGS1("UPCA", 12)},
		// UPC-E and EAN-8 are both 8 digits and both checksum-validated —
		// this is a genuine, irreducible overlap (not fixable by
		// reordering, unlike F1): most valid UPC-E codes are also
		// structurally valid EAN-8 (ut-docs#933 review finding F2). This
		// entry comes after EAN8, so an 8-digit code valid under both
		// types today. LookupKey is unaffected either way — only the
		// recorded barcode_type can be wrong for such a code — but a shop
		// that genuinely uses UPC-E should disable EAN8 if that matters
		// to it (ADR-0059 Decision §2's per-shop enabled set already
		// supports this; no code change needed here).
		{ID: "UPCE", NameKey: "barcode.symbology.UPCE", Parse: parseUPCE},
		// GTIN14 merges ITF-14 and GS1 DataBar-transmitted-as-GTIN-14: a
		// keyboard-wedge scanner reports only the decoded digit string,
		// never which symbology produced it, and both encode to the same
		// 14-digit mod-10-checked value — splitting them into two entries
		// would create an unresolvable ambiguity for zero benefit
		// (ADR-0059 Decision §1).
		{ID: "GTIN14", NameKey: "barcode.symbology.GTIN14", Parse: parsePlainGS1("GTIN14", 14)},
		// From here down: permissive catch-alls with no checksum, tried
		// only once nothing more specific has matched (see Match).
		{ID: "CODE128", NameKey: "barcode.symbology.CODE128", Parse: parseCode128},
		{ID: "CODE39", NameKey: "barcode.symbology.CODE39", Parse: parseCode39},
		{ID: "INTERNAL_PLU", NameKey: "barcode.symbology.INTERNAL_PLU", Parse: parseInternalPLU},
		{ID: "EAN13_WEIGHT_PREFIX2X", NameKey: "barcode.symbology.EAN13_WEIGHT_PREFIX2X", embedsData: true, Parse: parseEAN13WeightPrefix2x},
		{ID: "EAN13_PRICE_PREFIX02", NameKey: "barcode.symbology.EAN13_PRICE_PREFIX02", embedsData: true, Parse: parseEAN13PricePrefix02},
	}}
}

// ValidEAN13Checksum reports whether code is exactly 13 digits with a valid
// GS1 (EAN-13) check digit. Exported so internal/data's AddBarcode can
// share this instead of keeping its own copy (ADR-0059 Decision §1 —
// "reuse/extract ... rather than duplicating it"); the EAN13 Symbology
// above uses the same check under the hood.
func ValidEAN13Checksum(code string) bool {
	return len(code) == 13 && isAllDigits(code) && gs1CheckDigitValid(code)
}

// isAllDigits reports whether s is non-empty and every byte is '0'-'9'.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// digitsToInt converts an all-digit string to its integer value. Callers
// must already have checked s via isAllDigits — unlike strconv.Atoi, this
// has no error return, because that check makes failure unreachable
// (ut-docs#933 review finding F6: strconv.Atoi's error branch here was
// provably dead code, since every caller had already validated the input).
func digitsToInt(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// gs1CheckDigit computes the standard GS1 (EAN/UPC/ITF/GTIN family) mod-10
// check digit for body (all digits, the code with its own trailing check
// digit already stripped). The weighting (3, 1, alternating) is defined
// from the rightmost digit backward, so one formula is correct for every
// GS1 code length this package uses — EAN-8, UPC-A/12, EAN-13, GTIN-14 all
// share it; the existing internal/data validEAN13 (pre-ADR-0059) only
// worked for 13-digit codes because a 12-digit body happens to make the
// from-the-left and from-the-right indexing agree.
func gs1CheckDigit(body string) byte {
	sum := 0
	weight := 3
	for i := len(body) - 1; i >= 0; i-- {
		sum += int(body[i]-'0') * weight
		if weight == 3 {
			weight = 1
		} else {
			weight = 3
		}
	}
	return byte((10-sum%10)%10) + '0'
}

// gs1CheckDigitValid reports whether code's final digit is the correct GS1
// check digit for the digits preceding it. Callers must already have
// checked code is all-digit and at least 2 characters long.
func gs1CheckDigitValid(code string) bool {
	if len(code) < 2 {
		return false
	}
	return code[len(code)-1] == gs1CheckDigit(code[:len(code)-1])
}

// parsePlainGS1 builds a Parse func for a plain (no embedded data) GS1
// symbology identified purely by length + check digit: EAN-13, EAN-8,
// UPC-A and GTIN-14 (ITF-14/GS1 DataBar) all fit this shape.
func parsePlainGS1(id string, length int) func(string) (Decoded, bool) {
	return func(code string) (Decoded, bool) {
		if len(code) != length || !isAllDigits(code) || !gs1CheckDigitValid(code) {
			return Decoded{}, false
		}
		return Decoded{SymbologyID: id, LookupKey: code}, true
	}
}

// parseUPCE validates an 8-digit zero-suppressed UPC-E code (a number
// system digit, six compressed digits, then a check digit) by expanding it
// to its 12-digit UPC-A form and checking that form's GS1 check digit.
func parseUPCE(code string) (Decoded, bool) {
	if len(code) != 8 || !isAllDigits(code) {
		return Decoded{}, false
	}
	if code[0] != '0' && code[0] != '1' {
		return Decoded{}, false
	}
	expanded := upcEToUPCA(code)
	if !gs1CheckDigitValid(expanded) {
		return Decoded{}, false
	}
	return Decoded{SymbologyID: "UPCE", LookupKey: code}, true
}

// upcEToUPCA expands an 8-digit zero-suppressed UPC-E code (number-system
// digit + 6 compressed digits + check digit) to its 12-digit UPC-A form,
// per the standard UPC-E zero-suppression rules keyed on the last
// compressed digit. Caller guarantees code is 8 all-digit characters.
func upcEToUPCA(code string) string {
	ns := code[0:1]
	d := code[1:7]
	check := code[7:8]
	var body string
	switch d[5] {
	case '0', '1', '2':
		body = ns + d[0:2] + d[5:6] + "0000" + d[2:5]
	case '3':
		body = ns + d[0:3] + "00000" + d[3:5]
	case '4':
		body = ns + d[0:4] + "00000" + d[4:5]
	default: // '5'-'9'
		body = ns + d[0:5] + "0000" + d[5:6]
	}
	return body + check
}

// parseCode128 applies a structural check only, per ADR-0059 §1 — Code 128
// has no universal check digit. A keyboard-wedge scanner has already
// decoded it to text by the time it reaches this package, so the check here
// is simply "non-empty, printable ASCII" (the range a wedge can transmit),
// not a re-derivation of Code 128's own symbol/codeset encoding.
func parseCode128(code string) (Decoded, bool) {
	if code == "" {
		return Decoded{}, false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < 0x20 || code[i] > 0x7E {
			return Decoded{}, false
		}
	}
	return Decoded{SymbologyID: "CODE128", LookupKey: code}, true
}

// code39Charset is Code 39's standard 43-character symbol set (digits,
// upper-case letters, and a handful of punctuation) — Code 39 has no
// lower-case letters.
const code39Charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ-. $/+%"

// parseCode39 applies a structural check only, per ADR-0059 §1 — like Code
// 128, Code 39 has no universal check digit. The check here is
// "non-empty, every character in Code 39's symbol set."
func parseCode39(code string) (Decoded, bool) {
	if code == "" {
		return Decoded{}, false
	}
	for i := 0; i < len(code); i++ {
		if !strings.ContainsRune(code39Charset, rune(code[i])) {
			return Decoded{}, false
		}
	}
	return Decoded{SymbologyID: "CODE39", LookupKey: code}, true
}

// parseInternalPLU accepts any non-empty string with no further validation
// — a shop using this symbology is asserting the scan is one of its own
// internal/PLU codes, whatever shape that takes.
func parseInternalPLU(code string) (Decoded, bool) {
	if code == "" {
		return Decoded{}, false
	}
	return Decoded{SymbologyID: "INTERNAL_PLU", LookupKey: code}, true
}

// parseEAN13WeightPrefix2x parses the European scale-label convention:
// a 13-digit, check-digit-valid EAN-13 with prefix "20"-"29" (digits 1-2),
// a 5-digit item code (digits 3-7), and a 5-digit embedded weight in grams
// (digits 8-12, i.e. 3 implied decimal places when read as kilograms).
func parseEAN13WeightPrefix2x(code string) (Decoded, bool) {
	if len(code) != 13 || !isAllDigits(code) || !gs1CheckDigitValid(code) {
		return Decoded{}, false
	}
	if code[0] != '2' {
		return Decoded{}, false
	}
	grams := digitsToInt(code[7:12])
	return Decoded{
		SymbologyID: "EAN13_WEIGHT_PREFIX2X",
		// digits 1-7 (prefix+item code) kept; the weight (8-12) and check
		// digit (13) — both label-specific — are zeroed to a stable key.
		LookupKey:         code[0:7] + "000000",
		HasEmbeddedWeight: true,
		EmbeddedWeight:    fmt.Sprintf("%d.%03d", grams/1000, grams%1000),
	}, true
}

// parseEAN13PricePrefix02 parses the price-embedded convention: a 13-digit,
// check-digit-valid EAN-13 with a fixed "02" prefix, a 5-digit item code
// (digits 3-7), and a 5-digit embedded absolute price in the shop
// currency's minor units (digits 8-12).
func parseEAN13PricePrefix02(code string) (Decoded, bool) {
	if len(code) != 13 || !isAllDigits(code) || !gs1CheckDigitValid(code) {
		return Decoded{}, false
	}
	if code[0:2] != "02" {
		return Decoded{}, false
	}
	minorUnits := digitsToInt(code[7:12])
	return Decoded{
		SymbologyID: "EAN13_PRICE_PREFIX02",
		// digits 1-7 (prefix+item code) kept; the price (8-12) and check
		// digit (13) — both label-specific — are zeroed to a stable key.
		LookupKey:        code[0:7] + "000000",
		HasEmbeddedPrice: true,
		EmbeddedPrice:    money.FromMinor(int64(minorUnits)),
	}, true
}
