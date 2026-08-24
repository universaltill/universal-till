package catimport

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/barcode"
)

// testEnabledIDs is the full internal/barcode default registry, used as
// every test's enabledSymbologyIDs unless a test specifically exercises a
// narrower shop-enabled set (ADR-0059 Decision §3, ut-docs#936) — none of
// this file's fixture barcodes fall in the two embedded-data prefixes
// (20-29 weight, 02 price), so including them here doesn't change any
// existing test's expected outcome.
var testEnabledIDs = barcode.Default().IDs()

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
	res, err := Parse(strings.NewReader(loyverseCSV), 2, testEnabledIDs)
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
	res, err := Parse(strings.NewReader(squareCSV), 2, testEnabledIDs)
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
	res, err := Parse(strings.NewReader(sumupCSV), 2, testEnabledIDs)
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
	// it doesn't match the synonym set's "tax rate" exact/bracket-prefix
	// rule directly; it resolves via the paren-stripped fallback, ut-docs#587).
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

// Review finding N1 (ut-docs#587 → ut-docs#705): hasColumn strips a trailing
// parenthetical before matching synonyms, so a decorated header like
// "Dept (code)" now counts as a department column. DetectFormat's department
// case is checked before the "item id"+"variant id" sumup fallback signature,
// so a header set carrying BOTH the fallback signature and a paren-suffixed
// department header must resolve to generic-erp — department wins, extending
// the M1 "department wins over the sumup fallback" rule to the paren-lenient
// match. This pins that decision so it stays a choice, not an accident.
func TestDetectFormatParenSuffixDepartmentWinsOverSumUpFallback(t *testing.T) {
	// SumUp fallback signature (exact "item id"+"variant id") present, plus a
	// paren-suffixed department header: department must win → generic-erp.
	headers := []string{"Item name", "Price", "Item id", "Variant id", "Dept (code)"}
	if got := DetectFormat(headers); got != "generic-erp" {
		t.Errorf("DetectFormat(%v) = %s, want generic-erp (paren-suffixed department must win over the sumup fallback)", headers, got)
	}
	// Same signature but the department header carries no paren suffix — the
	// plain "Department" synonym already wins here; asserted so a future change
	// to hasColumn's paren handling can't silently diverge the two shapes.
	plain := []string{"Item name", "Price", "Item id", "Variant id", "Department"}
	if got := DetectFormat(plain); got != "generic-erp" {
		t.Errorf("DetectFormat(%v) = %s, want generic-erp", plain, got)
	}
	// Control: the same fallback signature with NO department column at all
	// (paren-suffixed or otherwise) still detects as sumup — the paren
	// leniency must not manufacture a department axis out of nothing.
	noDept := []string{"Item name", "Price", "Item id", "Variant id", "Notes (internal)"}
	if got := DetectFormat(noDept); got != "sumup" {
		t.Errorf("DetectFormat(%v) = %s, want sumup (no department column present)", noDept, got)
	}
}

func TestParseGenericAndZeroDecimals(t *testing.T) {
	res, err := Parse(strings.NewReader(genericCSV), 0, testEnabledIDs)
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
	res, err := Parse(strings.NewReader(erpCSV), 2, testEnabledIDs)
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

// ut-docs#587: headerIndex/hasColumn's matching was exact-or-bracket-prefix
// only, so header variants carrying a trailing parenthesised suffix
// ("(%)", a currency code, ...) missed their synonym entirely and the
// column was silently dropped (no TaxIssue — the column was never
// recognised in the first place, not found-but-unparseable).
func TestHeaderMatchingParenthesisedSuffix(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"no space before paren", "Tax rate(%)"},
		{"synonym only reachable via stripping, not a literal entry", "VAT rate (%)"},
		{"bare field name with paren suffix", "Tax (%)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			csv := "Name,Price," + c.header + "\nEspresso,2.00,19\n"
			res, err := Parse(strings.NewReader(csv), 2, testEnabledIDs)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(res.Items) != 1 {
				t.Fatalf("items = %d", len(res.Items))
			}
			it := res.Items[0]
			if !it.HasTax || it.TaxRateBP != 1900 {
				t.Errorf("header %q not recognised as the tax column: %+v", c.header, it)
			}
		})
	}
}

// The fix must be general (every columnSynonyms field), not a tax-only
// patch — prove it against an unrelated field.
func TestHeaderMatchingParenthesisedSuffixNonTaxField(t *testing.T) {
	res, err := Parse(strings.NewReader("Name,Price(GBP)\nWidget,4.50\n"), 2, testEnabledIDs)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d", len(res.Items))
	}
	if res.Items[0].PriceMinor != 450 {
		t.Errorf("\"Price(GBP)\" not recognised as the price column: %+v", res.Items[0])
	}
}

// Independent review finding B1 (ut-docs#587): a trailing parenthetical
// often QUALIFIES or NEGATES the synonym it's attached to, rather than
// being decorative — "Price (cost)", "VAT (takeaway)", "Stock (reserved)".
// The paren-stripped fallback must never outrank a genuine exact match
// found elsewhere on the row, or a file that imported correctly before
// this fix starts silently importing the wrong column's value: a cost
// price sold as the retail price, a takeaway tax rate booked as the
// dine-in rate — the exact silent-money/compliance-corruption class this
// package's other guardrails (ut-docs#512, #586) exist to prevent.
func TestHeaderMatchingParenSuffixNeverShadowsAnExactMatch(t *testing.T) {
	res, err := Parse(strings.NewReader("Name,Price (cost),Price\nWidget,1.10,4.50\n"), 2, testEnabledIDs)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d", len(res.Items))
	}
	if got := res.Items[0].PriceMinor; got != 450 {
		t.Errorf("exact \"Price\" column shadowed by the earlier lenient \"Price (cost)\" match: got %d, want 450 (the real retail price, not the 110 cost price)", got)
	}

	res, err = Parse(strings.NewReader("Name,Price,VAT (takeaway),VAT rate\nEspresso,2.00,7,19\n"), 2, testEnabledIDs)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d", len(res.Items))
	}
	if got := res.Items[0].TaxRateBP; got != 1900 {
		t.Errorf("exact \"VAT rate\" column shadowed by the earlier lenient \"VAT (takeaway)\" match: got %d bp, want 1900 (dine-in rate, not the 700bp takeaway rate)", got)
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
	res, err := Parse(strings.NewReader(taxCSV), 2, testEnabledIDs)
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
	res, err := Parse(strings.NewReader(loyverseCSV), 2, testEnabledIDs)
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

// TestParsePrice is ParsePrice's first direct unit test (ut-docs#586 —
// previously it was only exercised indirectly through CSV-level tests).
// The German decimal-comma cases are the bug under fix: "3,50" used to
// parse as 35000 (comma stripped as a thousands separator, 100x too high)
// and "1.234,50" as 123 (thousands dot misread as the decimal point). The
// zero-decimal-currency and repeated-thousands-separator cases are a
// second-round fix (independent review, ut-docs#586, finding #1/#2): a
// naive last-separator-wins reading silently under-parsed a plain
// thousands-grouped JPY/IRR/IRT/IQD/AFN price by 1000x, and turned a
// repeated English thousands comma into a hard error.
func TestParsePrice(t *testing.T) {
	cases := []struct {
		in       string
		decimals int
		want     int64
		wantErr  bool
	}{
		// German decimal-comma notation (the original ut-docs#586 fix).
		{"3,50", 2, 350, false},
		{"1.234,50", 2, 123450, false},
		// Regression: existing English thousands-comma behaviour.
		{"£1,234.50", 2, 123450, false},
		// Regression: existing plain dot-decimal behaviour.
		{"1.40", 2, 140, false},
		// Regression: plain integer, zero-decimal currency.
		{"12000", 0, 12000, false},
		// Zero-decimal currency, comma-thousands (review finding #1): a
		// currency with no fractional part can never have a genuine
		// decimal comma, so "12,000" IRR/IRT/IQD/AFN/JPY must read as
		// twelve thousand, not twelve.
		{"12,000", 0, 12000, false},
		{"980,000", 0, 980000, false},
		{"1,200", 0, 1200, false},
		// The same 3-digit shape on a currency that DOES have decimals
		// keeps the decimal-comma reading (the accepted ambiguity):
		// "2,900" reads as €2.900 ≈ €2.90, i.e. 290 minor units at
		// decimals=2.
		{"2,900", 2, 290, false},
		// Repeated thousands separator, one kind only (review finding #2):
		// can only be grouping, never a decimal point.
		{"1,234,567", 0, 1234567, false},
		{"12,345,678", 2, 1234567800, false},
		// Indian-style grouping (2-then-3 digit groups) still resolves as
		// thousands — the last group is what decides, not group count.
		{"1,23,456", 0, 123456, false},
		// Round-2 review finding N1: a zero-decimal currency's own
		// rendering puts the symbol/code AFTER the grouped number
		// (FormatMoney does this for IRR/IRT/IQD/AFN — "12,000 ریال").
		// The ambiguity check must look at the digit run right after the
		// separator, not demand the whole remainder be digits, or these
		// silently reinstate the 1000x bug the fix exists to remove.
		{"12,000 IRR", 0, 12000, false},
		{"12,000 ریال", 0, 12000, false},
		{"¥12,000", 0, 12000, false},
		// Round-2 review finding N2: a repeated separator that is NOT
		// clean 3-digit grouping must still fail loudly, not silently
		// squash into an absurd value. "12.05.2026" is a German date
		// leaking into a price column; "1.2.3" is column-mapping garbage.
		{"12.05.2026", 2, 0, true},
		{"1.2.3", 2, 0, true},
		// Error path must be untouched.
		{"abc", 2, 0, true},
	}
	for _, c := range cases {
		got, err := ParsePrice(c.in, c.decimals)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParsePrice(%q, %d) = %d, want error", c.in, c.decimals, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePrice(%q, %d): unexpected error %v", c.in, c.decimals, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParsePrice(%q, %d) = %d, want %d", c.in, c.decimals, got, c.want)
		}
	}
}

// TestNormalizeDecimalComma pins the separator-disambiguation heuristic
// itself: the documented accepted ambiguity ("2,900" reads as decimal-comma
// 2.900 for a non-zero-decimals currency, not thousands-grouped 2900 — see
// normalizeDecimalComma's doc comment), its currency-aware resolution for
// zero-decimal currencies, repeated-separator thousands grouping, and the
// no-separator fast path that must leave existing dot-only/no-separator
// inputs byte-identical.
func TestNormalizeDecimalComma(t *testing.T) {
	cases := []struct {
		in       string
		decimals int
		want     string
	}{
		{"1.40", 2, "1.40"},          // no comma: unchanged
		{"12000", 0, "12000"},        // no separator: unchanged
		{"", 2, ""},                  // empty: unchanged
		{"3,50", 2, "3.50"},          // comma-only: decimal comma
		{"1.234,50", 2, "1234.50"},   // German thousands + decimal comma
		{"£1,234.50", 2, "£1234.50"}, // dot last: comma is thousands
		{"2,900", 2, "2.900"},        // accepted ambiguity, decimals>0: decimal comma wins
		{"2,900", 0, "2900"},         // same shape, decimals=0: thousands wins (finding #1)
		{"12,000", 0, "12000"},       // zero-decimal currency thousands grouping
		{"1,234,567", 0, "1234567"},  // repeated comma: always thousands (finding #2)
		{"1.234.567", 2, "1234567"},  // repeated dot: always thousands
		// Round-2 finding N1: trailing symbol/code after the digits must
		// not block the ambiguity check.
		{"12,000 IRR", 0, "12000 IRR"},
		{"¥12,000", 0, "¥12000"},
		// Round-2 finding N2: repeated separator whose last group ISN'T
		// clean 3-digit grouping is left untouched, so downstream parsing
		// fails loudly instead of guessing.
		{"12.05.2026", 2, "12.05.2026"},
		{"1.2.3", 2, "1.2.3"},
	}
	for _, c := range cases {
		if got := normalizeDecimalComma(c.in, c.decimals); got != c.want {
			t.Errorf("normalizeDecimalComma(%q, %d) = %q, want %q", c.in, c.decimals, got, c.want)
		}
	}
}

func TestParseRejectsHeaderlessGarbage(t *testing.T) {
	if _, err := Parse(strings.NewReader("a,b,c\n1,2,3\n"), 2, testEnabledIDs); err == nil {
		t.Fatal("no name column must be an error")
	}
}

func TestNormalizeBarcode(t *testing.T) {
	// Under the DEFAULT enabled set (every permissive catch-all on),
	// Match never rejects a non-empty code (ADR-0059 §2) — this is the
	// ut-docs#936 acceptance criteria directly: a short numeric PLU and an
	// alphanumeric supplier code both now match (via CODE128's structural
	// catch-all), where the old ad hoc 6-14-digit-only rule discarded
	// both. Rejection is only reachable once a shop has narrowed its
	// enabled set away from the catch-alls (last case).
	cases := []struct {
		name       string
		in         string
		enabledIDs []string
		wantKey    string
		wantType   string
		wantOK     bool
	}{
		{"trailing .0 spreadsheet artifact stripped (all-digit remainder), then EAN13-matched", "5449000000996.0", testEnabledIDs, "5449000000996", "EAN13", true},
		{"4-digit produce PLU matches a default catch-all", "4011", testEnabledIDs, "4011", "CODE128", true},
		{"alphanumeric internal/supplier code matches a default catch-all", "SKU-ABC123", testEnabledIDs, "SKU-ABC123", "CODE128", true},
		// F2: an alphanumeric code that legitimately ends ".0" must NOT be
		// truncated — the remainder ("ABC") is not all-digit, so the ".0"
		// stays and the literal code is what gets stored and later scanned.
		{"non-numeric code ending .0 is not truncated", "ABC.0", testEnabledIDs, "ABC.0", "CODE128", true},
		// F4: spreadsheet scientific-notation mangling is not un-mangled;
		// under the default set CODE128's structural catch-all stores it
		// verbatim (the old ad hoc rule discarded it — deliberate ADR-0059
		// §2 trade-off, documented on normalizeBarcode).
		{"scientific-notation mangling stored verbatim under the default set", "5.449E+12", testEnabledIDs, "5.449E+12", "CODE128", true},
		{"empty raw value never matches anything, any enabled set", "", testEnabledIDs, "", "", false},
		{
			"narrowed enabled set (no catch-alls) rejects a shape matching nothing left",
			"12345", // 5 digits: not EAN13/EAN8/UPCA/UPCE/GTIN14-shaped
			[]string{"EAN13", "EAN8", "UPCA", "UPCE", "GTIN14"},
			"", "", false,
		},
	}
	for _, c := range cases {
		dec, gotOK := normalizeBarcode(c.in, c.enabledIDs)
		if dec.LookupKey != c.wantKey || dec.SymbologyID != c.wantType || gotOK != c.wantOK {
			t.Errorf("%s: normalizeBarcode(%q) = (key=%q, type=%q, %v), want (key=%q, type=%q, %v)",
				c.name, c.in, dec.LookupKey, dec.SymbologyID, gotOK, c.wantKey, c.wantType, c.wantOK)
		}
	}
}

// TestParseImportsShortAndAlphanumericBarcodes is the ut-docs#936
// acceptance criteria run through the full Parse row loop (not just
// normalizeBarcode directly): a 4-digit produce PLU and an alphanumeric
// supplier code both import end-to-end via internal/catimport with the
// barcode attached, once the shop's enabled set allows them — the old ad
// hoc 6-14-digit rule discarded both silently.
func TestParseImportsShortAndAlphanumericBarcodes(t *testing.T) {
	csv := `Name,Price,Barcode
Loose Tomatoes,1.50,4011
Supplier Widget,2.00,SKU-ABC123
`
	res, err := Parse(strings.NewReader(csv), 2, testEnabledIDs)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	if got := res.Items[0].Barcode; got != "4011" {
		t.Errorf("4-digit PLU barcode = %q, want %q", got, "4011")
	}
	if res.Items[0].BarcodeIssue != "" {
		t.Errorf("4-digit PLU must not carry a BarcodeIssue, got %q", res.Items[0].BarcodeIssue)
	}
	if got := res.Items[1].Barcode; got != "SKU-ABC123" {
		t.Errorf("alphanumeric barcode = %q, want %q", got, "SKU-ABC123")
	}
	if res.Items[1].BarcodeIssue != "" {
		t.Errorf("alphanumeric barcode must not carry a BarcodeIssue, got %q", res.Items[1].BarcodeIssue)
	}
}

// TestParseReportsNoSymbologyMatchReason confirms the row-level warning
// path (ut-docs#936's own acceptance criteria: "import row status reports
// the reason when a barcode is rejected by the shop's enabled set,
// consistent with #293's existing reason-reporting fix") — reachable only
// once a shop has narrowed its enabled set away from the default
// catch-alls, per ADR-0059 §2.
func TestParseReportsNoSymbologyMatchReason(t *testing.T) {
	narrowIDs := []string{"EAN13", "EAN8", "UPCA", "UPCE", "GTIN14"}
	csv := `Name,Price,Barcode
Mystery Item,1.00,12345
`
	res, err := Parse(strings.NewReader(csv), 2, narrowIDs)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	it := res.Items[0]
	if it.Barcode != "" {
		t.Errorf("unmatched barcode must not be stored, got %q", it.Barcode)
	}
	if it.BarcodeIssue != BarcodeIssueNoSymbologyMatch {
		t.Errorf("BarcodeIssue = %q, want %q", it.BarcodeIssue, BarcodeIssueNoSymbologyMatch)
	}
	if it.BarcodeIssueRaw != "12345" {
		t.Errorf("BarcodeIssueRaw = %q, want %q", it.BarcodeIssueRaw, "12345")
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
