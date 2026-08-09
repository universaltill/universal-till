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
	for _, in := range []string{"", "%", "abc", "-5", "19,5,0"} {
		if got, err := ParseTaxRateBP(in); err == nil {
			t.Errorf("ParseTaxRateBP(%q) = %d, want error", in, got)
		}
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
