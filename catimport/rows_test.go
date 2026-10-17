package catimport

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// externalIDs parses csv and returns each item's ExternalID in file order.
func externalIDs(t *testing.T, csv string) []string {
	t.Helper()
	res, err := Parse(strings.NewReader(csv), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := make([]string, len(res.Items))
	for i, it := range res.Items {
		out[i] = it.ExternalID
	}
	return out
}

func eqStrings(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s = %q, want %q", label, got, want)
	}
}

// ADR-0164 §1: external_id is set where the format has a stable id.
func TestExternalID_PerFormat(t *testing.T) {
	// Loyverse: Handle + SKU; a row with no SKU keeps the bare handle.
	eqStrings(t, "loyverse", externalIDs(t, loyverseCSV), []string{"coke/10001", "bananas/10002", "broken"})
	eqStrings(t, "square", externalIDs(t, squareCSV), []string{"tok1", "tok2"})
	eqStrings(t, "sumup", externalIDs(t, sumupCSV), []string{"1001/2001", "1002/2002", "1003/2003"})
	// Shopify: Handle + Variant SKU; a variant with no SKU keeps the bare
	// handle. Image-only continuation rows are dropped by Parse already.
	eqStrings(t, "shopify", externalIDs(t, shopifyCSV), []string{
		"cold-brew/SH-CB-1", "tee/SH-TEE-S-BLK", "tee/SH-TEE-M-BLK", "tee/SH-TEE-L-WHT", "no-sku-mug", "mystery-box/SH-MB-1",
	})
	// No stable id in a generic or ERP file.
	eqStrings(t, "generic", externalIDs(t, genericCSV), []string{""})
	for i, id := range externalIDs(t, erpCSV) {
		if id != "" {
			t.Errorf("erp row %d external id = %q, want empty", i, id)
		}
	}
}

// Loyverse ids are Handle/SKU on every row that has a SKU, whether or not
// the Handle repeats, so an id never depends on the rest of the file. A row
// with no SKU keeps the bare Handle; two such rows sharing a Handle keep
// the same id, and the consumer reports the repeat as a row issue
// (catsync: external_id_duplicate) rather than guessing.
func TestExternalID_LoyverseHandleAndSKU(t *testing.T) {
	csv := `Handle,SKU,Name,Category,Sold by weight,Price,Barcode,In stock
shirt,10001,Shirt S,Clothes,N,10.00,,1
shirt,10002,Shirt M,Clothes,N,10.00,,1
mug,10003,Mug,Home,N,5.00,,1
bag,,Bag,Home,N,1.00,,1
box,,Box A,Home,N,2.00,,1
box,,Box B,Home,N,2.00,,1
`
	eqStrings(t, "loyverse", externalIDs(t, csv), []string{"shirt/10001", "shirt/10002", "mug/10003", "bag", "box", "box"})
}

// ADR-0164 re-import idempotency: a later export that adds a sibling
// variant must not change the id of an item already imported.
func TestExternalID_StableWhenSiblingVariantAdded(t *testing.T) {
	before := `Handle,SKU,Name,Category,Sold by weight,Price,Barcode,In stock
shirt,10001,Shirt S,Clothes,N,10.00,,1
mug,10003,Mug,Home,N,5.00,,1
`
	after := `Handle,SKU,Name,Category,Sold by weight,Price,Barcode,In stock
shirt,10001,Shirt S,Clothes,N,10.00,,1
shirt,10002,Shirt M,Clothes,N,10.00,,1
mug,10003,Mug,Home,N,5.00,,1
`
	b, a := externalIDs(t, before), externalIDs(t, after)
	if b[0] != a[0] || b[1] != a[2] {
		t.Fatalf("ids changed when a variant was added: before %q, after %q", b, a)
	}
}

func TestExternalID_XLSX(t *testing.T) {
	f := excelize.NewFile()
	rows := [][]any{
		{"Token", "Item Name", "Variation Name", "SKU", "Category", "Price", "GTIN"},
		{"tok9", "Tea", "Regular", "SQ-9", "Hot", 2.5, ""},
	}
	for r, row := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, r+1)
		if err := f.SetSheetRow("Sheet1", cell, &row); err != nil {
			t.Fatal(err)
		}
	}
	var b bytes.Buffer
	if err := f.Write(&b); err != nil {
		t.Fatal(err)
	}
	res, err := ParseXLSX(bytes.NewReader(b.Bytes()), int64(b.Len()), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("parse xlsx: %v", err)
	}
	if res.Format != "square" || len(res.Items) != 1 || res.Items[0].ExternalID != "tok9" {
		t.Fatalf("xlsx square external id: format=%s items=%+v", res.Format, res.Items)
	}
}

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }
func boolp(v bool) *bool     { return &v }

// ToRows emits ADR-0051 item rows (the vocabulary catsync decodes):
// importable items only, money in minor units, optional fields only when
// the file carried them.
func TestToRows_Mapping(t *testing.T) {
	no, yes := false, true
	res := Result{Format: "generic-erp", Items: []ImportItem{
		{ // 0: full row
			ExternalID: "x1", Name: "Cola", SKU: "C-1", Barcode: "5449000000996", BarcodeType: "EAN13", PriceMinor: 140,
			Department: "Drinks", Category: "Soft", TaxRateBP: 1900, HasTax: true,
			Stock: 24, HasStock: true, IsWeighed: false,
			Description: "  Classic cola\nchilled  ", AgeRestricted: true,
			NetQuantityValue: i64(330), NetQuantityUnit: str("ml"),
		},
		{ // 1: minimal row, weighed
			Name: "Bananas", PriceMinor: 89, IsWeighed: true,
		},
		{ // 2: blocked row: never emitted
			Name: "", Issue: IssueMissingName,
		},
		{ // 3: the source said "don't track" — its quantity is not a stock level (ut-docs#1843)
			Name: "Bag", PriceMinor: 10, Stock: 0, HasStock: true, TracksStock: no, HasTracksStock: true,
		},
		{ // 4: tracked explicitly
			Name: "Tin", PriceMinor: 99, Stock: 3, HasStock: true, TracksStock: yes, HasTracksStock: true, Category: "Food",
		},
		{ // 5: gift voucher: no item_type in ADR-0051 rows, so it is not a row
			Name: "Voucher", ItemType: "gift_voucher",
		},
	}}
	got := ToRows(res)
	want := []Row{
		{ExternalID: "x1", Name: "Cola", SKU: "C-1", Barcode: "5449000000996", BarcodeType: "EAN13", PriceMinor: i64(140),
			CategoryPath: []string{"Drinks", "Soft"}, VATRateBP: i64(1900), StockQty: f64(24),
			Description: "Classic cola\nchilled", AgeRestricted: boolp(true),
			NetQuantityValue: i64(330), NetQuantityUnit: str("ml")},
		{Name: "Bananas", PriceMinor: i64(89), SoldByWeight: true},
		{Name: "Bag", PriceMinor: i64(10)},
		{Name: "Tin", PriceMinor: i64(99), CategoryPath: []string{"Food"}, StockQty: f64(3)},
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("ToRows:\n got %s\nwant %s", gj, wj)
	}
	// RowFor reports the skip, so a caller can count it against its item.
	if _, ok := RowFor(res.Items[2]); ok {
		t.Error("RowFor(blocked item) ok = true")
	}
	if _, ok := RowFor(res.Items[5]); ok {
		t.Error("RowFor(gift voucher) ok = true")
	}
}

// The JSON field names are ADR-0051's: a plugin's rows and the module's
// rows decode the same way (catsync's wire type is this type).
func TestRow_JSONVocabulary(t *testing.T) {
	b, err := json.Marshal(Row{ExternalID: "e", Name: "n", SKU: "s", Barcode: "b", Barcodes: []string{"c"},
		PriceMinor: i64(1), CategoryPath: []string{"k"}, VATRateBP: i64(2), StockQty: f64(3), SoldByWeight: true})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"external_id":"e","name":"n","sku":"s","barcode":"b","barcodes":["c"],"price_minor":1,"category_path":["k"],"vat_rate_bp":2,"stock_qty":3,"active":null,"sold_by_weight":true}`
	if string(b) != want {
		t.Errorf("json = %s\nwant  %s", b, want)
	}
}

// A source id longer than ADR-0161's 128-rune external_id bound would be
// refused by every consumer; it is dropped so the row still links by SKU.
func TestToRows_OverlongExternalIDDropped(t *testing.T) {
	long := strings.Repeat("h", 129)
	rows := ToRows(Result{Items: []ImportItem{{ExternalID: long, Name: "A", PriceMinor: 1}}})
	if len(rows) != 1 || rows[0].ExternalID != "" {
		t.Fatalf("rows = %+v, want one row with an empty external id", rows)
	}
}

// SKUs are never invented by the parser (#1459's generation is the
// consumer's, in the till's PreviewItemSKU format): a row without one
// stays without one, so the consumer can count what it generates.
func TestToRows_NoSKUStaysEmpty(t *testing.T) {
	rows := ToRows(Result{Items: []ImportItem{{Name: "A", PriceMinor: 1}}})
	if len(rows) != 1 || rows[0].SKU != "" {
		t.Fatalf("rows = %+v", rows)
	}
}

// barcode_type is additive (ADR-0051 §3): absent when empty (see
// TestRow_JSONVocabulary), and next to barcode when set.
func TestRow_JSONBarcodeType(t *testing.T) {
	b, err := json.Marshal(Row{ExternalID: "e", Name: "n", Barcode: "0212345000000", BarcodeType: "EAN13_PRICE_PREFIX02", PriceMinor: i64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"barcode":"0212345000000","barcode_type":"EAN13_PRICE_PREFIX02","barcodes":null`) {
		t.Errorf("json = %s, want barcode_type after barcode", b)
	}
}

// A price-embedded label is decoded once, here: the row carries the zeroed
// lookup key and the symbology that produced it, so a consumer stores it
// as typed instead of re-matching the key (ut-docs#936).
func TestToRows_PriceEmbeddedBarcodeKeepsType(t *testing.T) {
	csv := "Handle,SKU,Name,Category,Sold by weight,Price,Barcode,In stock\n" +
		"cheese,20001,Cheese,Deli,N,2.50,0212345002506,1\n"
	res, err := Parse(strings.NewReader(csv), 2, []string{"EAN13", "EAN13_PRICE_PREFIX02"}, false)
	if err != nil {
		t.Fatal(err)
	}
	rows := ToRows(res)
	if len(rows) != 1 || rows[0].Barcode != "0212345000000" || rows[0].BarcodeType != "EAN13_PRICE_PREFIX02" {
		t.Fatalf("rows = %+v, want key 0212345000000 typed EAN13_PRICE_PREFIX02", rows)
	}
}

// description, age_restricted and the net-quantity pair are additive
// (ADR-0051 §3, ut-docs#4542): absent when unset (TestRow_JSONVocabulary
// pins that), and named as below when set.
func TestRow_JSONDescriptionAgeNetQuantity(t *testing.T) {
	b, err := json.Marshal(Row{ExternalID: "e", Name: "n", PriceMinor: i64(1), Description: "d",
		AgeRestricted: boolp(false), NetQuantityValue: i64(250), NetQuantityUnit: str("g")})
	if err != nil {
		t.Fatal(err)
	}
	want := `"sold_by_weight":false,"description":"d","age_restricted":false,"net_quantity_value":250,"net_quantity_unit":"g"}`
	if !strings.HasSuffix(string(b), want) {
		t.Errorf("json = %s\nwant suffix %s", b, want)
	}
}

// A file can say "age restricted" but never "not restricted": a file
// without the column parses the same as one that says No. So RowFor emits
// age_restricted only as true, and a row from an unrestricted item leaves
// the consumer's flag as it is (ut-docs#4542).
func TestRowFor_AgeRestrictedOnlyWhenTrue(t *testing.T) {
	r, ok := RowFor(ImportItem{Name: "Tea", PriceMinor: 1})
	if !ok || r.AgeRestricted != nil || r.Description != "" || r.NetQuantityValue != nil || r.NetQuantityUnit != nil {
		t.Fatalf("plain item row = %+v, want no description/age/net quantity", r)
	}
	r, _ = RowFor(ImportItem{Name: "Wine", PriceMinor: 1, AgeRestricted: true})
	if r.AgeRestricted == nil || !*r.AgeRestricted {
		t.Fatalf("restricted item row age_restricted = %v, want true", r.AgeRestricted)
	}
}

// The till's own export headers round-trip through rows: what the file
// importer would store, a row carries.
func TestToRows_TillExportColumns(t *testing.T) {
	csv := "Name,Price,Description,Age restricted,Net quantity,Net quantity unit\n" +
		"Red wine,9.99,Dry red,Y,750,ml\n" +
		"Water,0.50,,N,,\n"
	res, err := Parse(strings.NewReader(csv), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatal(err)
	}
	rows := ToRows(res)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	w := rows[0]
	if w.Description != "Dry red" || w.AgeRestricted == nil || !*w.AgeRestricted ||
		w.NetQuantityValue == nil || *w.NetQuantityValue != 750 || w.NetQuantityUnit == nil || *w.NetQuantityUnit != "ml" {
		t.Errorf("wine row = %+v", w)
	}
	if v := rows[1]; v.Description != "" || v.AgeRestricted != nil || v.NetQuantityValue != nil || v.NetQuantityUnit != nil {
		t.Errorf("water row = %+v, want no description/age/net quantity", v)
	}
}
