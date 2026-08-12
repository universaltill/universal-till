package catimport

import (
	"strings"
	"testing"
)

const loyverseCSV = `Handle,SKU,Name,Category,Sold by weight,Price,Cost,Barcode,In stock
coke,10001,Coca-Cola 330ml,Drinks,N,1.40,0.80,5449000000996,24
bananas,10002,Bananas,Produce,Y,0.89,0.40,,12.5
broken,,,,N,abc,,,
`

const squareCSV = "\uFEFF" + `Token,Item Name,Variation Name,SKU,Description,Category,Price,GTIN
tok1,Coffee,Large,SQ-1,Fresh brew,Hot Drinks,3.50,
tok2,Sandwich,Regular,SQ-2,,Food,"£4,250.00",00012345678905
`

const genericCSV = `Product Name,Retail Price,EAN,Category
Widget,2.00,4006381333931,Bits
`

// An ERP master export: distinct department + category axes, opening stock
// (decimal for weighed goods), and ERP-flavoured column names.
const erpCSV = `Item Name,SKU,Barcode,Price,Department,Category,On Hand,Weighed (Y/N)
Ripe Bananas,PLU-100,,0.89,Produce,Fruit,12.5,Y
LED Bulb,SKU-200,5000000000011,3.00,Electrical,Lighting,40,N
Loose Screws,SKU-300,,0.05,Hardware,,1000,N
`

func TestParseLoyverse(t *testing.T) {
	res, err := Parse(strings.NewReader(loyverseCSV), 2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Format != "loyverse" {
		t.Errorf("format = %s", res.Format)
	}
	if len(res.Items) != 3 {
		t.Fatalf("items = %d", len(res.Items))
	}
	coke := res.Items[0]
	if coke.Name != "Coca-Cola 330ml" || coke.PriceMinor != 140 || coke.Barcode != "5449000000996" ||
		coke.SKU != "10001" || coke.Category != "Drinks" || coke.IsWeighed || coke.Issue != "" {
		t.Errorf("coke parsed wrong: %+v", coke)
	}
	if !res.Items[1].IsWeighed {
		t.Error("bananas should be weighed")
	}
	if res.Items[2].Issue == "" {
		t.Error("broken row must carry an issue")
	}
}

func TestParseSquare(t *testing.T) {
	res, err := Parse(strings.NewReader(squareCSV), 2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Format != "square" {
		t.Errorf("format = %s", res.Format)
	}
	if res.Items[0].Name != "Coffee Large" {
		t.Errorf("variation not appended: %q", res.Items[0].Name)
	}
	// "Regular" variation is not appended; thousands+symbol price parses.
	if res.Items[1].Name != "Sandwich" || res.Items[1].PriceMinor != 425000 {
		t.Errorf("sandwich parsed wrong: %+v", res.Items[1])
	}
	if res.Items[1].Barcode != "00012345678905" {
		t.Errorf("gtin not mapped: %q", res.Items[1].Barcode)
	}
}

// A representative slice of SumUp's real 46-column items-export header
// (ut-docs#581) — column names only, never real business data (the café's
// actual export is deliberately kept out of this repo). Item names/prices
// are synthetic; the (19,7) row on Milchkaffee reproduces the real dine-in
// vs. takeaway split #512 exists to carry through correctly.
const sumupCSV = `Item name,Variations,Option set 1,Is variation visible?,Price,Cost price,Tax rate (%),Set up different prices and VAT for takeaway,Takeaway price,Takeaway tax rate,Unit,SKU,Barcode,Category,Item id,Variant id
Cappuccino,,,,3.20,1.10,19.00,Y,3.20,19.00,pcs,SU-001,,Kaffee,1001,2001
Käsekuchen,,,,3.80,1.50,7.00,Y,3.80,7.00,pcs,SU-002,,Kuchen & Desserts,1002,2002
Milchkaffee to go,,,,3.50,1.20,19.00,Y,3.50,7.00,pcs,SU-003,4006381333931,Kaltgetränke,1003,2003
`

func TestParseSumUp(t *testing.T) {
	res, err := Parse(strings.NewReader(sumupCSV), 2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Format != "sumup" {
		t.Errorf("format = %s, want sumup (must not fall through to generic)", res.Format)
	}
	if len(res.Items) != 3 {
		t.Fatalf("items = %d", len(res.Items))
	}
	cappuccino := res.Items[0]
	if cappuccino.Name != "Cappuccino" || cappuccino.PriceMinor != 320 || cappuccino.Category != "Kaffee" {
		t.Errorf("cappuccino parsed wrong: %+v", cappuccino)
	}
	if !cappuccino.HasTax || cappuccino.TaxRateBP != 1900 || !cappuccino.HasTakeaway || cappuccino.TakeawayRateBP != 1900 {
		t.Errorf("cappuccino tax (19,19) via \"Tax rate (%%)\" wrong: %+v", cappuccino)
	}
	// Umlaut category name must survive intact, and "Tax rate (%)" must be
	// recognised as the tax column (the whole reason this fixture exists —
	// it doesn't match the pre-existing synonym set's "tax rate" exact/
	// bracket-prefix rule).
	kaesekuchen := res.Items[1]
	if kaesekuchen.Name != "Käsekuchen" || kaesekuchen.Category != "Kuchen & Desserts" {
		t.Errorf("umlaut name/category mangled: %+v", kaesekuchen)
	}
	if !kaesekuchen.HasTax || kaesekuchen.TaxRateBP != 700 || !kaesekuchen.HasTakeaway || kaesekuchen.TakeawayRateBP != 700 {
		t.Errorf("käsekuchen tax (7,7) wrong: %+v", kaesekuchen)
	}
	// The real (dine-in, takeaway) override case: 19 in store, 7 to go.
	milchkaffee := res.Items[2]
	if milchkaffee.Barcode != "4006381333931" || milchkaffee.Category != "Kaltgetränke" {
		t.Errorf("milchkaffee barcode/category wrong: %+v", milchkaffee)
	}
	if !milchkaffee.HasTax || milchkaffee.TaxRateBP != 1900 || !milchkaffee.HasTakeaway || milchkaffee.TakeawayRateBP != 700 {
		t.Errorf("milchkaffee (19,7) takeaway override not carried through: %+v", milchkaffee)
	}
}

func TestDetectFormatSumUpFallbackSignature(t *testing.T) {
	// A merchant export missing the takeaway-VAT-toggle column (that toggle
	// is itself optional in a merchant's SumUp account) must still detect
	// via the "item id"+"variant id" fallback signature, not fall through
	// to generic.
	headers := []string{"Item name", "Price", "Tax rate (%)", "SKU", "Item id", "Variant id"}
	if got := DetectFormat(headers); got != "sumup" {
		t.Errorf("DetectFormat fallback signature = %s, want sumup", got)
	}
}

// Review finding M1 (ut-docs#581, 2026-08-12): an ERP master export whose ID
// columns merely contain the substrings "item id"/"variant id" (e.g. "Item
// Identifier", "Parent Item Id") must not be stolen from generic-erp by the
// SumUp fallback signature — department wins, and the fallback signature
// itself must be an exact per-header match, not a joined-string substring
// search.
func TestDetectFormatGenericERPNotStolenBySumUpFallback(t *testing.T) {
	cases := [][]string{
		{"Item Name", "Item ID", "Variant ID", "Price", "Department", "Category", "On Hand"},
		{"Item Name", "Item Identifier", "Variant Identifier", "Price", "Department"},
		{"Item Name", "Parent Item Id", "Child Variant Ids", "Price", "Department"},
	}
	for _, headers := range cases {
		if got := DetectFormat(headers); got != "generic-erp" {
			t.Errorf("DetectFormat(%v) = %s, want generic-erp (department must win over the sumup fallback)", headers, got)
		}
	}
	// The substring-bleed shape without a department column has nothing to
	// fall back to and must land on generic, not a false-positive sumup.
	noDept := []string{"Item Name", "Item Identifier", "Variant Identifier", "Price"}
	if got := DetectFormat(noDept); got != "generic" {
		t.Errorf("DetectFormat(%v) = %s, want generic", noDept, got)
	}
}

func TestParseGenericAndZeroDecimals(t *testing.T) {
	res, err := Parse(strings.NewReader(genericCSV), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Format != "generic" {
		t.Errorf("format = %s", res.Format)
	}
	it := res.Items[0]
	if it.Name != "Widget" || it.PriceMinor != 2 || it.Barcode != "4006381333931" || it.Category != "Bits" {
		t.Errorf("generic parsed wrong: %+v", it)
	}
	if it.Department != "" {
		t.Errorf("plain category must not populate department: %q", it.Department)
	}
}

func TestParseGenericERPStockAndDepartment(t *testing.T) {
	res, err := Parse(strings.NewReader(erpCSV), 2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Format != "generic-erp" {
		t.Errorf("format = %s, want generic-erp", res.Format)
	}
	if len(res.Items) != 3 {
		t.Fatalf("items = %d", len(res.Items))
	}
	// Weighed produce: decimal opening stock, distinct dept+category axes.
	bananas := res.Items[0]
	if bananas.Department != "Produce" || bananas.Category != "Fruit" {
		t.Errorf("dept/category axes wrong: %+v", bananas)
	}
	if !bananas.HasStock || bananas.Stock != 12.5 {
		t.Errorf("decimal opening stock not parsed: %+v", bananas)
	}
	if !bananas.IsWeighed {
		t.Error("bananas should be weighed")
	}
	// Whole-unit stock via the "On Hand" synonym.
	if res.Items[1].Stock != 40 || !res.Items[1].HasStock {
		t.Errorf("on-hand stock wrong: %+v", res.Items[1])
	}
	// Department present, category absent — department left standing alone.
	screws := res.Items[2]
	if screws.Department != "Hardware" || screws.Category != "" {
		t.Errorf("department-only row wrong: %+v", screws)
	}
	if screws.Stock != 1000 {
		t.Errorf("large stock wrong: %+v", screws)
	}
}

// The real café rate distribution behind ut-docs#512: four (dine-in, takeaway)
// pairs — (19,7) needs an override, (7,7)/(19,19)/(0,0) don't — plus a blank
// cell and an unparseable cell. The parser stays mechanical: it carries both
// rates raw and never blocks a row on a bad tax cell (compliance-sensitive,
// so the drop must be visible — TaxIssue — but the row still imports).
const taxCSV = `Name,Price,Tax rate,Takeaway tax
Latte,3.50,19,7
Cake Slice,4.00,7,7
Logo Mug,9.00,19,19
Gift Voucher,10.00,0,0
Mystery Item,1.00,,
Bad Tax,2.00,abc,7
Bad Takeaway,2.50,19,wat
`

func TestParseTaxColumns(t *testing.T) {
	res, err := Parse(strings.NewReader(taxCSV), 2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Items) != 7 {
		t.Fatalf("items = %d", len(res.Items))
	}
	latte := res.Items[0]
	if !latte.HasTax || latte.TaxRateBP != 1900 || !latte.HasTakeaway || latte.TakeawayRateBP != 700 {
		t.Errorf("latte (19,7) parsed wrong: %+v", latte)
	}
	cake := res.Items[1]
	if !cake.HasTax || cake.TaxRateBP != 700 || !cake.HasTakeaway || cake.TakeawayRateBP != 700 {
		t.Errorf("cake (7,7) parsed wrong: %+v", cake)
	}
	mug := res.Items[2]
	if !mug.HasTax || mug.TaxRateBP != 1900 || !mug.HasTakeaway || mug.TakeawayRateBP != 1900 {
		t.Errorf("mug (19,19) parsed wrong: %+v", mug)
	}
	voucher := res.Items[3]
	if !voucher.HasTax || voucher.TaxRateBP != 0 || !voucher.HasTakeaway || voucher.TakeawayRateBP != 0 {
		t.Errorf("voucher (0,0) parsed wrong: %+v", voucher)
	}
	// Blank cells: both flags false, no issue — the row imports at the
	// till's default rate exactly as before this column existed.
	mystery := res.Items[4]
	if mystery.HasTax || mystery.HasTakeaway || mystery.TaxIssue != "" || mystery.TakeawayTaxIssue != "" {
		t.Errorf("blank tax cells must stay silent: %+v", mystery)
	}
	// A present-but-unparseable dine-in cell: non-blocking issue, HasTax
	// stays false so the row still imports at the default rate.
	badTax := res.Items[5]
	if badTax.HasTax || badTax.TaxIssue != TaxIssueUnparseable || badTax.TaxIssueRaw != "abc" {
		t.Errorf("unparseable tax cell must set TaxIssue, not block: %+v", badTax)
	}
	if badTax.Issue != "" {
		t.Errorf("a bad tax cell must never block the row: %+v", badTax)
	}
	if !badTax.HasTakeaway || badTax.TakeawayRateBP != 700 {
		t.Errorf("takeaway cell must still parse when the dine-in cell is bad: %+v", badTax)
	}
	// Same treatment for the takeaway column.
	badTA := res.Items[6]
	if !badTA.HasTax || badTA.TaxRateBP != 1900 {
		t.Errorf("dine-in cell must still parse when the takeaway cell is bad: %+v", badTA)
	}
	if badTA.HasTakeaway || badTA.TakeawayTaxIssue != TaxIssueUnparseable || badTA.TakeawayTaxIssueRaw != "wat" {
		t.Errorf("unparseable takeaway cell must set TakeawayTaxIssue: %+v", badTA)
	}
}

// A file with no tax column at all (every pre-existing fixture) must leave
// the tax fields untouched — existing Loyverse/Square imports unchanged.
func TestParseNoTaxColumnLeavesTaxUnset(t *testing.T) {
	res, err := Parse(strings.NewReader(loyverseCSV), 2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for i, it := range res.Items {
		if it.HasTax || it.HasTakeaway || it.TaxIssue != "" || it.TakeawayTaxIssue != "" {
			t.Errorf("row %d: tax fields must stay zero without a tax column: %+v", i, it)
		}
	}
}

func TestParseTaxRateBP(t *testing.T) {
	good := map[string]int{
		"19":    1900,
		"19%":   1900,
		"19 %":  1900,
		" 7 ":   700,
		"19.5":  1950,
		"19.5%": 1950,
		"0":     0,
		"0%":    0,
		"8.875": 888, // rounds to the nearest basis point
	}
	for in, want := range good {
		got, err := ParseTaxRateBP(in)
		if err != nil || got != want {
			t.Errorf("ParseTaxRateBP(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "%", "abc", "-5", "19,5,0",
		// Review finding B1 (ut-docs#512, 2026-08-09): strconv.ParseFloat
		// happily accepts these, and neither is < 0, so they used to sail
		// past the guard and round to math.MinInt64 — a persisted
		// tax_codes.rate_basis_points of -9223372036854775808, silently,
		// on the exact compliance-sensitive path this card exists to
		// protect. Must all be rejected, same as any other garbage input.
		"NaN", "nan", "Inf", "+Inf", "-Inf", "infinity",
		// Also review finding B1: a rate over 100% is never a real VAT
		// percentage — "1900" (a merchant typing basis points where a
		// percentage was expected) and "1e3" (scientific notation) must
		// not silently create a 1900%/1000% tax code.
		"1900", "1e3", "101",
	} {
		if got, err := ParseTaxRateBP(in); err == nil {
			t.Errorf("ParseTaxRateBP(%q) = %d, want error", in, got)
		}
	}
	// 100 itself is a legitimate (if extreme) boundary — must not be
	// rejected by the new upper bound.
	if got, err := ParseTaxRateBP("100"); err != nil || got != 10000 {
		t.Errorf("ParseTaxRateBP(\"100\") = %d, %v; want 10000, nil", got, err)
	}
}

func TestParseRejectsHeaderlessGarbage(t *testing.T) {
	if _, err := Parse(strings.NewReader("a,b,c\n1,2,3\n"), 2); err == nil {
		t.Fatal("no name column must be an error")
	}
}

func TestNormalizeBarcode(t *testing.T) {
	cases := map[string]string{
		"5449000000996.0": "5449000000996",
		"5.449E+12":       "",
		"12345":           "", // too short
		"abc":             "",
	}
	for in, want := range cases {
		if got := normalizeBarcode(in); got != want {
			t.Errorf("normalizeBarcode(%q) = %q, want %q", in, got, want)
		}
	}
}

// stripCSVDefuse is the reverse of pages.csvSafe (ut-docs#321): it only
// strips a leading "'" when the byte right after it is itself one of
// csvSafe's own trigger chars — the exact two-byte pattern csvSafe emits,
// and one a genuine hand-typed value beginning with an apostrophe would
// only produce by coincidence. That's what makes stripping safe: it can't
// be fooled by an ordinary value that merely happens to start with "'".
func TestStripCSVDefuse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text", "Sparkling Water 500ml", "Sparkling Water 500ml"},
		// These also cover the accepted lossy collision (2026-08-06 review,
		// see stripCSVDefuse's doc comment): csvSafe("'=X") == csvSafe("=X")
		// == "'=X", so a pre-existing "'=X"-shaped value is indistinguishable
		// from our own defuse marker and strips the same way. Pinned here
		// deliberately, not just incidentally exercised.
		{"defused equals formula (or a pre-existing '=-shaped value — indistinguishable, by design)", `'=cmd|'/c calc'!A1`, `=cmd|'/c calc'!A1`},
		{"defused plus formula", "'+1+1", "+1+1"},
		{"defused minus formula", "'-1+1", "-1+1"},
		{"defused at formula", "'@SUM(A1:A2)", "@SUM(A1:A2)"},
		{"defused leading tab", "'\tsneaky", "\tsneaky"},
		{"defused leading CR", "'\rsneaky", "\rsneaky"},
		{"genuine leading apostrophe, not our marker", "'Twas the night", "'Twas the night"},
		{"lone apostrophe", "'", "'"},
		{"lone hyphen sentinel — never defused, so never stripped", "-", "-"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripCSVDefuse(c.in); got != c.want {
				t.Errorf("stripCSVDefuse(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
