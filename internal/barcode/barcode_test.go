package barcode

import (
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ValidEAN13Checksum is exercised elsewhere in this file only indirectly
// (every plain/embedded EAN-13-shaped Parse calls it under the hood) — this
// is a direct test of the exported function itself, the whole point of
// ut-docs#933's extraction from internal/data/catalog_repo.go.
func TestValidEAN13Checksum(t *testing.T) {
	if !ValidEAN13Checksum(validEAN13) {
		t.Errorf("ValidEAN13Checksum(%q) = false, want true", validEAN13)
	}
	if ValidEAN13Checksum("4006381333932") {
		t.Error("ValidEAN13Checksum accepted a corrupted check digit")
	}
	if ValidEAN13Checksum("400638133393") { // 12 digits, wrong length
		t.Error("ValidEAN13Checksum accepted a 12-digit string")
	}
	if ValidEAN13Checksum("400638133393X") { // non-digit
		t.Error("ValidEAN13Checksum accepted a non-digit character")
	}
}

// Test vectors below are either real, commonly-published valid barcodes
// (EAN-13, EAN-8, UPC-A — cross-checked against the shared gs1CheckDigit
// formula) or derived from that same formula (EAN-8... GTIN-14, UPC-E, the
// two embedded-data symbologies) since no real scanner hardware is
// available to this pipeline; each "known-bad" vector is the matching
// known-good vector with its check digit deliberately corrupted.
const (
	validEAN13    = "4006381333931" // real, widely-published example
	validEAN8     = "96385210"
	validUPCA     = "036000291452" // real, widely-published example
	validUPCE     = "01234531"     // expands to UPC-A 012300000451
	validGTIN14   = "12345678901231"
	validWeight2x = "2012345012349" // prefix 20, item 12345, weight 01234 (=1.234kg)
	validPrice02  = "0298765003507" // prefix 02, item 98765, price 00350 (=350 minor units)
)

func TestPlainSymbologies(t *testing.T) {
	cases := []struct {
		id        string
		good      string
		bad       string
		noBadCase bool
	}{
		{id: "EAN13", good: validEAN13, bad: "4006381333932"},
		{id: "EAN8", good: validEAN8, bad: "96385211"},
		{id: "UPCA", good: validUPCA, bad: "036000291453"},
		{id: "UPCE", good: validUPCE, bad: "01234530"},
		{id: "CODE128", good: "Item-42_ABC", bad: "bad\x01code"},
		{id: "CODE39", good: "ABC-123", bad: "abc-123"}, // Code 39 has no lower-case
		{id: "GTIN14", good: validGTIN14, bad: "12345678901232"},
		// INTERNAL_PLU has no known-bad case by definition — see below.
		{id: "INTERNAL_PLU", good: "anything-goes-1", noBadCase: true},
	}

	reg := Default()
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			sym, ok := reg.Lookup(tc.id)
			if !ok {
				t.Fatalf("registry has no entry %q", tc.id)
			}
			d, ok := sym.Parse(tc.good)
			if !ok {
				t.Fatalf("Parse(%q) = false, want true", tc.good)
			}
			if d.SymbologyID != tc.id {
				t.Errorf("SymbologyID = %q, want %q", d.SymbologyID, tc.id)
			}
			if d.LookupKey != tc.good {
				t.Errorf("LookupKey = %q, want %q (plain symbology keeps the code as-is)", d.LookupKey, tc.good)
			}
			if tc.noBadCase {
				return
			}
			if _, ok := sym.Parse(tc.bad); ok {
				t.Errorf("Parse(%q) = true, want false (bad check digit/structure)", tc.bad)
			}
		})
	}
}

// TestUPCEZeroSuppressionBranches exercises all four zero-suppression
// branches of the standard UPC-E encoding (keyed on the last compressed
// digit: 0-2, 3, 4, 5-9), not just the case-3 vector validUPCE already
// covers (ut-docs#933 review finding F4). Each vector's UPC-A expansion is
// noted for anyone cross-checking against the standard.
func TestUPCEZeroSuppressionBranches(t *testing.T) {
	cases := []struct {
		name         string
		code         string // UPC-E, last compressed digit selects the branch
		expandedUPCA string
	}{
		{name: "last digit 0-2", code: "04567802", expandedUPCA: "045000006782"},
		{name: "last digit 3", code: validUPCE, expandedUPCA: "012300000451"},
		{name: "last digit 4", code: "01234941", expandedUPCA: "012340000091"},
		{name: "last digit 5-9", code: "01234572", expandedUPCA: "012345000072"},
	}
	sym, _ := Default().Lookup("UPCE")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := sym.Parse(tc.code); !ok {
				t.Fatalf("Parse(%q) = false, want true (expands to UPC-A %q)", tc.code, tc.expandedUPCA)
			}
			// A corrupted check digit must be rejected.
			bad := tc.code[:7] + string('0'+(tc.code[7]-'0'+1)%10)
			if _, ok := sym.Parse(bad); ok {
				t.Errorf("Parse(%q) = true, want false (corrupted check digit)", bad)
			}
		})
	}
}

func TestInternalPLUAcceptsAnyNonEmptyString(t *testing.T) {
	sym, _ := Default().Lookup("INTERNAL_PLU")
	// Including a 13-digit string that fails the EAN-13 check digit.
	d, ok := sym.Parse("4006381333932")
	if !ok {
		t.Fatal("INTERNAL_PLU rejected a 13-digit string — it must accept any non-empty string")
	}
	if d.LookupKey != "4006381333932" {
		t.Errorf("LookupKey = %q, want the code unchanged", d.LookupKey)
	}
	if _, ok := sym.Parse(""); ok {
		t.Error("INTERNAL_PLU accepted an empty string")
	}
}

func TestEAN13WeightPrefix2x(t *testing.T) {
	sym, _ := Default().Lookup("EAN13_WEIGHT_PREFIX2X")

	d, ok := sym.Parse(validWeight2x)
	if !ok {
		t.Fatalf("Parse(%q) = false, want true", validWeight2x)
	}
	if !d.HasEmbeddedWeight {
		t.Fatal("HasEmbeddedWeight = false, want true")
	}
	if d.EmbeddedWeight != "1.234" {
		t.Errorf("EmbeddedWeight = %q, want %q", d.EmbeddedWeight, "1.234")
	}
	if d.HasEmbeddedPrice {
		t.Error("HasEmbeddedPrice = true, want false")
	}
	const wantKey = "2012345" + "000000"
	if d.LookupKey != wantKey {
		t.Errorf("LookupKey = %q, want %q (weight+check zeroed)", d.LookupKey, wantKey)
	}

	// Known-bad: corrupted check digit.
	if _, ok := sym.Parse("2012345012340"); ok {
		t.Error("Parse accepted a corrupted check digit")
	}

	// A plain (non-2X-prefixed) EAN-13 must be rejected by this entry —
	// it still matches plain EAN13 instead (tested via the registry in
	// TestSpecificityOrder / TestPlainRejectedByEmbeddedEntries).
	if _, ok := sym.Parse(validEAN13); ok {
		t.Errorf("Parse(%q) = true, want false (prefix %q is not 20-29)", validEAN13, validEAN13[0:2])
	}
}

func TestEAN13PricePrefix02(t *testing.T) {
	sym, _ := Default().Lookup("EAN13_PRICE_PREFIX02")

	d, ok := sym.Parse(validPrice02)
	if !ok {
		t.Fatalf("Parse(%q) = false, want true", validPrice02)
	}
	if !d.HasEmbeddedPrice {
		t.Fatal("HasEmbeddedPrice = false, want true")
	}
	if d.EmbeddedPrice != money.FromMinor(350) {
		t.Errorf("EmbeddedPrice = %v, want 350 minor units", d.EmbeddedPrice)
	}
	if d.HasEmbeddedWeight {
		t.Error("HasEmbeddedWeight = true, want false")
	}
	const wantKey = "0298765" + "000000"
	if d.LookupKey != wantKey {
		t.Errorf("LookupKey = %q, want %q (price+check zeroed)", d.LookupKey, wantKey)
	}

	// Known-bad: corrupted check digit.
	if _, ok := sym.Parse("0298765003500"); ok {
		t.Error("Parse accepted a corrupted check digit")
	}

	// A plain (non-02-prefixed) EAN-13 must be rejected by this entry.
	if _, ok := sym.Parse(validEAN13); ok {
		t.Errorf("Parse(%q) = true, want false (prefix %q is not 02)", validEAN13, validEAN13[0:2])
	}
}

// TestSpecificityOrder is the regression test for the bug the independent
// review of ADR-0059's first draft caught: with both a plain and an
// embedded-data entry enabled, a code that is structurally valid under
// both must resolve to the more specific embedded-data entry, never plain
// EAN13 — see ADR-0059 Decision §3.
func TestSpecificityOrder(t *testing.T) {
	reg := Default()

	t.Run("weight", func(t *testing.T) {
		d, ok := reg.Match([]string{"EAN13", "EAN13_WEIGHT_PREFIX2X"}, validWeight2x)
		if !ok {
			t.Fatalf("Match(%q) = false, want true", validWeight2x)
		}
		if d.SymbologyID != "EAN13_WEIGHT_PREFIX2X" {
			t.Errorf("SymbologyID = %q, want EAN13_WEIGHT_PREFIX2X (must not fall through to plain EAN13)", d.SymbologyID)
		}
	})

	t.Run("price", func(t *testing.T) {
		d, ok := reg.Match([]string{"EAN13", "EAN13_PRICE_PREFIX02"}, validPrice02)
		if !ok {
			t.Fatalf("Match(%q) = false, want true", validPrice02)
		}
		if d.SymbologyID != "EAN13_PRICE_PREFIX02" {
			t.Errorf("SymbologyID = %q, want EAN13_PRICE_PREFIX02 (must not fall through to plain EAN13)", d.SymbologyID)
		}
	})

	// A code that is NOT prefix-2X/02 still matches plain EAN13 when both
	// tiers are enabled — the embedded entries don't swallow everything.
	t.Run("plain code still resolves to plain EAN13", func(t *testing.T) {
		d, ok := reg.Match([]string{"EAN13", "EAN13_WEIGHT_PREFIX2X", "EAN13_PRICE_PREFIX02"}, validEAN13)
		if !ok {
			t.Fatalf("Match(%q) = false, want true", validEAN13)
		}
		if d.SymbologyID != "EAN13" {
			t.Errorf("SymbologyID = %q, want EAN13", d.SymbologyID)
		}
	})
}

// TestGTIN14SingleMatch confirms the ADR-0059 GTIN14 merge: a code that
// would separately satisfy an ITF-14 and a GS1-DataBar-as-GTIN-14 reading
// resolves to exactly one registry entry, never an ambiguous/duplicate
// match between two GTIN-14-shaped entries — because there is exactly one
// GTIN14 entry in the registry, not two. (CODE128/CODE39/INTERNAL_PLU are
// deliberately permissive catch-alls and also parse a plain digit string —
// that overlap is by design, per ADR-0059 Decision §2, and not what this
// test is checking for.)
func TestGTIN14SingleMatch(t *testing.T) {
	reg := Default()
	matches := 0
	for _, id := range reg.IDs() {
		if id == "GTIN14" {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("registry has %d entries named GTIN14, want exactly 1", matches)
	}
	sym, _ := reg.Lookup("GTIN14")
	if _, ok := sym.Parse(validGTIN14); !ok {
		t.Errorf("GTIN14.Parse(%q) = false, want true", validGTIN14)
	}
}

// TestMatchGTIN14ResolvesWithFullDefaultSet is the regression test for
// review finding F1: with every default entry enabled (the realistic
// "shop hasn't touched settings" case), a valid GTIN-14 must resolve to
// GTIN14, not silently to CODE128 or CODE39 (both of which also accept a
// bare digit string structurally, and both used to be declared before
// GTIN14 in Default()). This exercises Match directly, unlike
// TestGTIN14SingleMatch above which only checks Parse in isolation and so
// could not have caught this — see the ut-docs#933 review record.
func TestMatchGTIN14ResolvesWithFullDefaultSet(t *testing.T) {
	reg := Default()
	d, ok := reg.Match(reg.IDs(), validGTIN14)
	if !ok {
		t.Fatalf("Match(%q) = false, want true", validGTIN14)
	}
	if d.SymbologyID != "GTIN14" {
		t.Errorf("SymbologyID = %q, want GTIN14 (got swallowed by a permissive catch-all declared earlier)", d.SymbologyID)
	}
}

// TestMatchEAN8UPCEOverlapIsDocumentedNotAccidental pins down the
// irreducible EAN8/UPC-E overlap review finding F2: "01234565" is a real,
// commonly-cited UPC-E code whose 8 digits also happen to satisfy EAN-8's
// own checksum directly. No declaration order removes this ambiguity (see
// Match's doc comment) — this test exists so a future reordering of
// Default() changes this outcome deliberately, with this test updated to
// match, rather than silently.
func TestMatchEAN8UPCEOverlapIsDocumentedNotAccidental(t *testing.T) {
	const overlapping = "01234565"
	reg := Default()

	// Confirm the premise: this code really does satisfy both entries'
	// own Parse independently.
	ean8, _ := reg.Lookup("EAN8")
	if _, ok := ean8.Parse(overlapping); !ok {
		t.Fatalf("premise broken: %q is not a valid EAN8", overlapping)
	}
	upce, _ := reg.Lookup("UPCE")
	if _, ok := upce.Parse(overlapping); !ok {
		t.Fatalf("premise broken: %q is not a valid UPCE", overlapping)
	}

	d, ok := reg.Match([]string{"EAN8", "UPCE"}, overlapping)
	if !ok {
		t.Fatalf("Match(%q) = false, want true", overlapping)
	}
	if d.SymbologyID != "EAN8" {
		t.Errorf("SymbologyID = %q, want EAN8 (Default()'s declared order — a shop that actually uses UPC-E should disable EAN8)", d.SymbologyID)
	}
}

// TestMatchOnlyTriesEnabledEntries confirms Match never reaches a
// registry entry that isn't in the enabled set, even when the scanned code
// would also satisfy that entry's own Parse. validEAN13 is deliberately
// used here: it's a numeric string, which CODE128's permissive structural
// check accepts too, so a naive implementation that ignored the enabled
// set could still "coincidentally" pass this test by matching EAN13
// directly — asserting the winning SymbologyID is CODE128, not EAN13,
// rules that out.
func TestMatchOnlyTriesEnabledEntries(t *testing.T) {
	reg := Default()
	d, ok := reg.Match([]string{"CODE128"}, validEAN13)
	if !ok {
		t.Fatal("Match = false, want true (CODE128 accepts any printable ASCII, including digits)")
	}
	if d.SymbologyID != "CODE128" {
		t.Errorf("SymbologyID = %q, want CODE128 (EAN13 must never be tried when not enabled)", d.SymbologyID)
	}
}

func TestMatchNoEnabledSymbologyMatches(t *testing.T) {
	reg := Default()
	if _, ok := reg.Match([]string{"EAN13"}, "not-a-valid-ean13"); ok {
		t.Error("Match(unmatchable code) = true, want false")
	}
}
