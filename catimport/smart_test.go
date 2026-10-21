package catimport

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// All headers and rows below are invented (ut-docs#4747); the Epos Now
// header is the one the card quotes, the rows are made up.

const eposHeader = "Name;CategoryId;SalePriceExTax;CostPriceIncTax;SalePriceIncTax;ButtonColourId;Barcode;Description;CostPriceExTax;SalePriceTaxGroupId;IsVariablePrice;TaxExemptEligible;ContainerFeeId;BrandName;SupplierName;VariantDescription;ColourName;SellOnTill;TillOrder;RRPrice;OrderCode;ArticleCode;PopupNoteId;MultiChoiceNoteId;Margin;IsStockTracked;ImageUrl"

// eposLine renders one 27-column row from the named columns; the rest are
// empty, as in a real export.
func eposLine(t *testing.T, cells map[string]string) string {
	t.Helper()
	cols := strings.Split(eposHeader, ";")
	if len(cols) != 27 {
		t.Fatalf("header has %d columns, want 27", len(cols))
	}
	out := make([]string, len(cols))
	known := map[string]bool{}
	for i, c := range cols {
		known[c] = true
		out[i] = cells[c]
	}
	for k := range cells {
		if !known[k] {
			t.Fatalf("fixture names unknown column %q", k)
		}
	}
	return strings.Join(out, ";")
}

func eposFile(t *testing.T, rows ...map[string]string) string {
	lines := []string{eposHeader}
	for _, r := range rows {
		lines = append(lines, eposLine(t, r))
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

func issuesByCode(res []RowIssue, code string) []RowIssue {
	var out []RowIssue
	for _, i := range res {
		if i.Code == code {
			out = append(out, i)
		}
	}
	return out
}

func smartOpts() Options {
	return Options{CurrencyDecimals: 2, EnabledSymbologyIDs: testEnabledIDs, KnownTaxRatesBP: []int{0, 500, 2000}}
}

func TestDetectSource(t *testing.T) {
	cases := []struct {
		name    string
		headers string
		want    string
	}{
		{"epos now products", eposHeader, SourceEposNow},
		{"epos now, folded case and BOM", "\ufeff" + strings.ToLower(eposHeader), SourceEposNow},
		{"epos now missing a signature column", "Name;CategoryId;SalePriceIncTax;Barcode", SourceGeneric},
		{"till export", "Name,SKU,Barcode,Price,Category,Description,Sold by weight,In stock,Active,Tax rate,Takeaway tax,Net quantity,Net quantity unit,Age restricted,Item type", SourceTillExport},
		{"my export", "id,sku,name,price,category,barcodes,color,active,sold_by_weight,track_stock,modifier_groups,stock,icon", SourceMyExport},
		{"my export, partial", "sku,name,price,sold_by_weight,track_stock", SourceMyExport},
		{"loyverse stays loyverse", "Handle,SKU,Name,Sold by weight,Active", FormatLoyverse},
		{"plain generic", "Name,Price", SourceGeneric},
		{"german generic", "Bezeichnung;Preis", SourceGeneric},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sep := ","
			if strings.Contains(c.headers, ";") {
				sep = ";"
			}
			if got := DetectSource(strings.Split(c.headers, sep)); got != c.want {
				t.Fatalf("DetectSource = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDetectKind(t *testing.T) {
	cases := []struct {
		name    string
		headers []string
		kind    string
		refusal string
	}{
		{"products en", []string{"Name", "Price"}, KindProducts, ""},
		{"products with quantity", []string{"Item name", "Retail price", "Quantity"}, KindProducts, ""},
		{"products de", []string{"Artikelbezeichnung", "Verkaufspreis", "Warengruppe"}, KindProducts, ""},
		{"products tr", []string{"Ürün Adı", "Satış Fiyatı", "Barkod"}, KindProducts, ""},
		{"products tr upper-case dotted I", []string{"ÜRÜN ADI", "FİYAT"}, KindProducts, ""},
		{"epos now is products", strings.Split(eposHeader, ";"), KindProducts, ""},
		{"my export is products", []string{"id", "sku", "name", "price", "sold_by_weight"}, KindProducts, ""},
		{"customers en", []string{"Customer Name", "Email", "Phone"}, KindCustomers, ""},
		{"customers name + postcode", []string{"Name", "Post code"}, KindCustomers, ""},
		{"customers de", []string{"Vorname", "Nachname", "E-Mail", "Telefon", "PLZ"}, KindCustomers, ""},
		{"customers tr", []string{"Ad Soyad", "E-posta", "Telefon"}, KindCustomers, ""},
		{"stock sku + quantity", []string{"SKU", "Quantity"}, KindStock, ""},
		{"stock barcode + count", []string{"Barcode", "Counted"}, KindStock, ""},
		{"stock de", []string{"Artikelnummer", "Bestand"}, KindStock, ""},
		{"stock tr", []string{"Stok Kodu", "Miktar"}, KindStock, ""},
		{"stock item code + on hand", []string{"Item code", "On hand"}, KindStock, ""},
		{"mixed customer columns and a price", []string{"Name", "Email", "Price"}, "", RefusalMixedKinds},
		{"mixed de", []string{"Kunde", "E-Mail", "Preis"}, "", RefusalMixedKinds},
		{"unknown", []string{"Foo", "Bar", "Baz"}, "", RefusalUnknownKind},
		{"name only is unknown", []string{"Name"}, "", RefusalUnknownKind},
		{"empty", nil, "", RefusalUnknownKind},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := DetectKind(c.headers)
			if d.Kind != c.kind || d.Refusal != c.refusal {
				t.Fatalf("DetectKind = kind %q refusal %q, want %q %q (%+v)", d.Kind, d.Refusal, c.kind, c.refusal, d)
			}
			if c.kind != "" && len(d.Matched) == 0 {
				t.Errorf("a detected kind names the columns it matched: %+v", d)
			}
			if c.kind != "" && len(d.Missing) != 0 {
				t.Errorf("a detected kind misses nothing: %+v", d)
			}
		})
	}
}

func TestDetectKindUnknownNamesMissingColumnsPerKind(t *testing.T) {
	d := DetectKind([]string{"Foo", "Quantity"})
	if d.Refusal != RefusalUnknownKind || d.Kind != "" {
		t.Fatalf("got %+v", d)
	}
	want := map[string][]string{
		KindProducts:  {"name", "price"},
		KindCustomers: {"name", "email|phone|postcode|address"},
		KindStock:     {"sku|barcode"},
	}
	if !reflect.DeepEqual(d.MissingByKind, want) {
		t.Fatalf("MissingByKind = %v, want %v", d.MissingByKind, want)
	}
}

func TestParseProductsEposNowFixture(t *testing.T) {
	long := strings.Repeat("Long name ", 30)
	file := eposFile(t,
		// row 2: the first of a duplicate barcode, with an article code
		map[string]string{"Name": "Flat White", "CategoryId": "Hot Drinks", "SalePriceExTax": "2.50", "SalePriceIncTax": "3.00",
			"CostPriceExTax": "0.80", "CostPriceIncTax": "0.96", "Barcode": "1000000000011", "SalePriceTaxGroupId": "20VAT",
			"SellOnTill": "yes", "IsStockTracked": "yes", "ArticleCode": "FW-1", "BrandName": "House", "SupplierName": "Beans Ltd",
			"Description": "Double shot", "ImageUrl": "https://img.example/fw.png", "IsVariablePrice": "no"},
		// row 3: duplicate NAME (a distinct item), empty barcode, no article code
		map[string]string{"Name": "Flat White", "CategoryId": "Cold Drinks", "SalePriceExTax": "2.50", "SalePriceIncTax": "3.00",
			"SalePriceTaxGroupId": "20VAT", "SellOnTill": "yes", "IsStockTracked": "no"},
		// row 4: duplicate barcode of row 2, empty category, hidden from the till, compare-at
		map[string]string{"Name": "Oat Bar", "CategoryId": "", "SalePriceExTax": "1.00", "SalePriceIncTax": "1.05",
			"Barcode": "1000000000011", "SalePriceTaxGroupId": "5VAT", "SellOnTill": "no", "RRPrice": "1.20", "IsStockTracked": "yep"},
		// row 5: zero price
		map[string]string{"Name": "Tap Water", "CategoryId": "Hot Drinks", "SalePriceExTax": "0.00", "SalePriceIncTax": "0.00",
			"SalePriceTaxGroupId": "0VAT", "SellOnTill": "yes", "IsStockTracked": "no"},
		// row 6: a tax rate the store does not have
		map[string]string{"Name": "Pasty", "CategoryId": "Food", "SalePriceExTax": "1.00", "SalePriceIncTax": "1.10",
			"Barcode": "1000000000028", "SalePriceTaxGroupId": "10VAT", "ArticleCode": "PA-1"},
		// row 7: a name past the limit
		map[string]string{"Name": long, "CategoryId": "Food", "SalePriceExTax": "5.00", "SalePriceIncTax": "6.00", "Barcode": "1000000000035", "SalePriceTaxGroupId": "20VAT", "ArticleCode": "LN-1"},
		// row 8: an unreadable price
		map[string]string{"Name": "Mystery", "CategoryId": "Food", "SalePriceExTax": "x", "SalePriceIncTax": "-3.00", "ArticleCode": "MY-1"},
	)
	if strings.HasPrefix(file, "\ufeff") || !strings.Contains(file, "\r\n") {
		t.Fatal("fixture must be UTF-8 without BOM and CRLF")
	}
	o := smartOpts()
	o.KnownCategories = []string{"Hot Drinks", "Food"}
	res, err := ParseProducts(strings.NewReader(file), o)
	if err != nil {
		t.Fatalf("ParseProducts: %v", err)
	}
	if res.Format != FormatEposNow || res.Source != SourceEposNow || res.Kind != KindProducts {
		t.Fatalf("format/source/kind = %q/%q/%q", res.Format, res.Source, res.Kind)
	}
	if len(res.Items) != 7 {
		t.Fatalf("items = %d, want 7 (a duplicate barcode or name never drops a row)", len(res.Items))
	}
	it := res.Items
	wantPrice := []int64{300, 300, 105, 0, 110, 600, 0}
	for i, w := range wantPrice[:6] {
		if it[i].PriceMinor != w {
			t.Errorf("item %d (%s) price = %d, want %d", i, it[i].Name, it[i].PriceMinor, w)
		}
	}
	for i := 0; i < 3; i++ {
		if it[i].PriceMinor == 0 {
			t.Errorf("item %d is a free item", i)
		}
	}
	if it[0].Category != "Hot Drinks" || it[1].Category != "Cold Drinks" || it[2].Category != "" {
		t.Errorf("categories = %q %q %q", it[0].Category, it[1].Category, it[2].Category)
	}
	if it[0].SKU != "FW-1" || it[1].SKU != "" {
		t.Errorf("SKU comes from ArticleCode only: %q %q", it[0].SKU, it[1].SKU)
	}
	if it[0].ExternalID != "" {
		t.Errorf("external id must stay empty, got %q", it[0].ExternalID)
	}
	// tax derived from inc/ex, snapped to the store's rates
	for i, bp := range []int{2000, 2000, 500} {
		if !it[i].HasTax || it[i].TaxRateBP != bp {
			t.Errorf("item %d tax = %v/%d, want %d", i, it[i].HasTax, it[i].TaxRateBP, bp)
		}
	}
	if !it[0].HasCost || it[0].CostMinor != 80 {
		t.Errorf("cost = %v/%d, want ex-tax 80", it[0].HasCost, it[0].CostMinor)
	}
	if it[0].TaxGroup != "20VAT" || it[0].PriceExTaxMinor != 250 {
		t.Errorf("kept tax group/ex price = %q/%d", it[0].TaxGroup, it[0].PriceExTaxMinor)
	}
	if !it[2].HasCompareAt || it[2].CompareAtMinor != 120 {
		t.Errorf("compare-at = %v/%d", it[2].HasCompareAt, it[2].CompareAtMinor)
	}
	if !it[2].HasActive || it[2].Active || !it[0].HasActive || !it[0].Active {
		t.Errorf("SellOnTill=no must hide the item: %v/%v %v/%v", it[2].HasActive, it[2].Active, it[0].HasActive, it[0].Active)
	}
	if !it[0].HasTracksStock || !it[0].TracksStock || !it[1].HasTracksStock || it[1].TracksStock {
		t.Errorf("IsStockTracked misread")
	}
	if it[0].Brand != "House" || it[0].Supplier != "Beans Ltd" || it[0].Description != "Double shot" || it[0].ImageURL != "https://img.example/fw.png" {
		t.Errorf("brand/supplier/description/image misread: %+v", it[0])
	}
	if it[0].Name != it[1].Name {
		t.Errorf("a duplicate name keeps both items")
	}

	// duplicate barcode: an issue on the later row, the first row keeps it
	dup := issuesByCode(res.Issues, IssueCodeDuplicateBarcode)
	if len(dup) != 1 || dup[0].Row != 4 || dup[0].Field != "barcode" || dup[0].Original != "1000000000011" ||
		dup[0].Suggested == nil || *dup[0].Suggested != "" || dup[0].Severity != SeverityWarning {
		t.Fatalf("duplicate barcode issues = %+v", dup)
	}
	if it[0].Barcode != "1000000000011" || it[2].Barcode != "" {
		t.Errorf("first row keeps the barcode, the repeat does not: %q %q", it[0].Barcode, it[2].Barcode)
	}
	// no issue is raised for a duplicate name or an empty category
	for _, i := range res.Issues {
		if i.Field == "name" && i.Code != IssueCodeNameTooLong {
			t.Errorf("unexpected name issue %+v", i)
		}
		if i.Field == "category" && i.Row == 4 {
			t.Errorf("an empty category is not an issue: %+v", i)
		}
	}
	// empty barcode (row 3)
	if mb := issuesByCode(res.Issues, IssueCodeMissingBarcode); len(mb) < 1 || mb[0].Row != 3 || mb[0].Field != "barcode" {
		t.Errorf("missing barcode issues = %+v", mb)
	}
	// generated SKU suggestions: one per row without an article code, all distinct
	skus := issuesByCode(res.Issues, IssueCodeMissingSKU)
	seen := map[string]bool{}
	for _, i := range skus {
		if i.Suggested == nil || *i.Suggested == "" || seen[*i.Suggested] {
			t.Errorf("missing_sku needs a distinct suggestion: %+v", i)
		}
		if i.Row == 2 || i.Row == 6 {
			t.Errorf("row %d has an article code", i.Row)
		}
		seen[*i.Suggested] = true
	}
	if len(skus) != 3 { // rows 3, 4, 5
		t.Errorf("missing_sku issues = %d, want 3", len(skus))
	}
	// unknown category -> create it
	uc := issuesByCode(res.Issues, IssueCodeUnknownCategory)
	if len(uc) != 1 || uc[0].Row != 3 || uc[0].Original != "Cold Drinks" || uc[0].Suggested == nil || *uc[0].Suggested != "Cold Drinks" {
		t.Errorf("unknown category issues = %+v", uc)
	}
	// zero price
	if z := issuesByCode(res.Issues, IssueCodeZeroPrice); len(z) != 1 || z[0].Row != 5 || z[0].Severity != SeverityWarning {
		t.Errorf("zero price issues = %+v", z)
	}
	// unknown tax rate -> nearest known rate (5), item untaxed until accepted
	ut := issuesByCode(res.Issues, IssueCodeUnknownTaxRate)
	if len(ut) != 1 || ut[0].Row != 6 || ut[0].Field != "tax" || ut[0].Original != "10VAT" || ut[0].Suggested == nil || *ut[0].Suggested != "5" {
		t.Errorf("unknown tax issues = %+v", ut)
	}
	if it[4].HasTax {
		t.Errorf("an unrecognised rate is never guessed into the item")
	}
	// long name -> shortened
	ln := issuesByCode(res.Issues, IssueCodeNameTooLong)
	if len(ln) != 1 || ln[0].Row != 7 || ln[0].Suggested == nil || len([]rune(*ln[0].Suggested)) > NameMaxRunes || *ln[0].Suggested == "" {
		t.Errorf("long name issues = %+v", ln)
	}
	// bad boolean -> parsed guess, row still imports with the guess flagged
	bb := issuesByCode(res.Issues, IssueCodeBadBoolean)
	if len(bb) != 1 || bb[0].Row != 4 || bb[0].Field != "track_stock" || bb[0].Original != "yep" || bb[0].Suggested == nil || *bb[0].Suggested != "yes" {
		t.Errorf("bad boolean issues = %+v", bb)
	}
	// bad number -> error with the best guess; the row is blocked, not dropped
	bp := issuesByCode(res.Issues, IssueCodeBadPrice)
	if len(bp) != 1 || bp[0].Row != 8 || bp[0].Severity != SeverityError || bp[0].Original != "-3.00" || bp[0].Suggested == nil || *bp[0].Suggested != "3.00" {
		t.Errorf("bad price issues = %+v", bp)
	}
	if it[6].Issue != IssueBadPrice {
		t.Errorf("item issue = %q", it[6].Issue)
	}
	// every issue has the fixed shape
	for _, i := range res.Issues {
		if i.Row < 2 || i.Code == "" || i.Field == "" || (i.Severity != SeverityError && i.Severity != SeverityWarning) {
			t.Errorf("malformed issue %+v", i)
		}
	}
}

func TestParseProductsEposNowSnapsAmbiguousAndZeroPriceTax(t *testing.T) {
	o := smartOpts()
	o.KnownTaxRatesBP = []int{500, 600, 2000}
	file := eposFile(t,
		// 5% and 6% both fit within a penny of 0.50 -> 0.53; the group id breaks the tie
		map[string]string{"Name": "A", "SalePriceExTax": "0.50", "SalePriceIncTax": "0.53", "SalePriceTaxGroupId": "6VAT", "ArticleCode": "A", "Barcode": "1000000000011"},
		// the same numbers with no usable group: ambiguous, never guessed
		map[string]string{"Name": "B", "SalePriceExTax": "0.50", "SalePriceIncTax": "0.53", "SalePriceTaxGroupId": "Std", "ArticleCode": "B", "Barcode": "1000000000028"},
		// zero price: the group id alone supplies the rate
		map[string]string{"Name": "C", "SalePriceExTax": "0.00", "SalePriceIncTax": "0.00", "SalePriceTaxGroupId": "20VAT", "ArticleCode": "C", "Barcode": "1000000000035"},
	)
	res, err := ParseProducts(strings.NewReader(file), o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Items[0].HasTax || res.Items[0].TaxRateBP != 600 {
		t.Errorf("tie broken by the group id: %v/%d", res.Items[0].HasTax, res.Items[0].TaxRateBP)
	}
	if res.Items[1].HasTax {
		t.Errorf("ambiguous rate must not be guessed")
	}
	if a := issuesByCode(res.Issues, IssueCodeAmbiguousVAT); len(a) != 1 || a[0].Row != 3 {
		t.Errorf("ambiguous_vat issues = %+v", a)
	}
	if !res.Items[2].HasTax || res.Items[2].TaxRateBP != 2000 {
		t.Errorf("zero price takes the group's rate: %v/%d", res.Items[2].HasTax, res.Items[2].TaxRateBP)
	}
}

func TestParseProductsEposNowBOMAndNoStoreRates(t *testing.T) {
	file := "\ufeff" + eposFile(t, map[string]string{"Name": "Tea", "CategoryId": "Hot", "SalePriceExTax": "1.67", "SalePriceIncTax": "2.00",
		"SalePriceTaxGroupId": "20VAT", "ArticleCode": "T", "Barcode": "1000000000011"})
	res, err := ParseProducts(strings.NewReader(file), Options{CurrencyDecimals: 2, EnabledSymbologyIDs: testEnabledIDs})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].PriceMinor != 200 || !res.Items[0].HasTax || res.Items[0].TaxRateBP != 2000 {
		t.Fatalf("%+v", res.Items)
	}
	if len(res.Issues) != 0 {
		t.Fatalf("a clean row has no issues: %+v", res.Issues)
	}
}

func TestParseProductsGenericLocaleAliases(t *testing.T) {
	cases := map[string]struct {
		file             string
		name, cat, price string
		sku, barcode     string
	}{
		"german":  {"Artikelbezeichnung;Verkaufspreis;Warengruppe;Artikelnummer;EAN\r\nTee;2,50;Getränke;T1;1000000000011\r\n", "Tee", "Getränke", "250", "T1", "1000000000011"},
		"turkish": {"Ürün Adı;Satış Fiyatı;Kategori;Stok Kodu;Barkod\r\nÇay;12,50;İçecek;C1;1000000000011\r\n", "Çay", "İçecek", "1250", "C1", "1000000000011"},
		"english": {"Item name,Retail price,Category,Item code,Barcode\nTea,2.50,Drinks,T1,1000000000011\n", "Tea", "Drinks", "250", "T1", "1000000000011"},
	}
	for n, c := range cases {
		t.Run(n, func(t *testing.T) {
			res, err := ParseProducts(strings.NewReader(c.file), smartOpts())
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Items) != 1 {
				t.Fatalf("items = %d", len(res.Items))
			}
			it := res.Items[0]
			if it.Name != c.name || it.Category != c.cat || it.SKU != c.sku || it.Barcode != c.barcode {
				t.Errorf("got %+v", it)
			}
			if got := it.PriceMinor; got != mustMinor(t, c.price) {
				t.Errorf("price = %d, want %s", got, c.price)
			}
		})
	}
}

func mustMinor(t *testing.T, s string) int64 {
	t.Helper()
	v, err := ParsePrice(s, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseProductsTillAndMyExportShapes(t *testing.T) {
	till := "Name,SKU,Barcode,Price,Category,Description,Sold by weight,In stock,Active,Tax rate,Takeaway tax,Net quantity,Net quantity unit,Age restricted,Item type\n" +
		"Cola,CO-1,1000000000011,1.50,Drinks,,N,12,N,20%,,,,N,standard\n"
	res, err := ParseProducts(strings.NewReader(till), smartOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceTillExport || len(res.Items) != 1 {
		t.Fatalf("%q %d", res.Source, len(res.Items))
	}
	if it := res.Items[0]; it.PriceMinor != 150 || !it.HasStock || it.Stock != 12 || !it.HasActive || it.Active || it.TaxRateBP != 2000 {
		t.Errorf("till export row misread: %+v", it)
	}
	my := "id,sku,name,price,category,barcodes,color,active,sold_by_weight,track_stock,modifier_groups,stock,icon\n" +
		"abc,SK-1,Apples,2.00,Fruit,1000000000011;1000000000028,,true,true,yes,,5,\n"
	res, err = ParseProducts(strings.NewReader(my), smartOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceMyExport || len(res.Items) != 1 {
		t.Fatalf("%q %d", res.Source, len(res.Items))
	}
	if it := res.Items[0]; !it.IsWeighed || !it.HasTracksStock || !it.TracksStock || !it.HasActive || !it.Active || it.SKU != "SK-1" || it.PriceMinor != 200 {
		t.Errorf("my export row misread: %+v", it)
	}
}

func TestParseRefusesWrongKindNamingColumns(t *testing.T) {
	var ke *KindError
	_, err := ParseProducts(strings.NewReader("Customer Name,Email,Price\nAnn,a@example.test,1.00\n"), smartOpts())
	if !errors.As(err, &ke) || ke.Code != RefusalMixedKinds {
		t.Fatalf("mixed file: %v", err)
	}
	_, err = ParseProducts(strings.NewReader("Foo,Bar\n1,2\n"), smartOpts())
	if !errors.As(err, &ke) || ke.Code != RefusalUnknownKind || len(ke.Detection.MissingByKind) != 3 {
		t.Fatalf("unknown file: %v", err)
	}
	_, err = ParseStock(strings.NewReader("Name,Price\nTea,1.00\n"), smartOpts())
	if !errors.As(err, &ke) || ke.Code != RefusalMissingColumns || ke.Kind != KindStock || !reflect.DeepEqual(ke.Missing, []string{"sku|barcode", "quantity"}) {
		t.Fatalf("stock on a products file: %v / %+v", err, ke)
	}
	_, err = ParseCustomers(strings.NewReader("Name,Price\nTea,1.00\n"), smartOpts())
	if !errors.As(err, &ke) || ke.Code != RefusalMissingColumns || ke.Kind != KindCustomers {
		t.Fatalf("customers on a products file: %v", err)
	}
}

func TestParseCustomers(t *testing.T) {
	file := "Vorname;Nachname;E-Mail;Telefon;PLZ;Straße\r\n" +
		"Ann;Example;ann@example.test;+44 20 7946 0000;AB1 2CD;1 Test Road\r\n" +
		"Bob;Sample;ANN@example.test;;;\r\n" + // same email, folded
		";;;+44 20 7946 1111;;\r\n" + // a phone and no name
		"Cy;Nobody;not-an-email;;;\r\n" +
		"=cmd;Evil;evil@example.test;;;\r\n"
	res, err := ParseCustomers(strings.NewReader(file), smartOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Source == "" || len(res.Customers) != 5 {
		t.Fatalf("customers = %d (%q)", len(res.Customers), res.Source)
	}
	c := res.Customers
	if c[0].Name != "Ann Example" || c[0].Email != "ann@example.test" || c[0].Phone != "+44 20 7946 0000" || c[0].Postcode != "AB1 2CD" || c[0].Address != "1 Test Road" {
		t.Errorf("first customer: %+v", c[0])
	}
	if c[0].Row != 2 || c[1].Row != 3 {
		t.Errorf("rows = %d %d", c[0].Row, c[1].Row)
	}
	if d := issuesByCode(res.Issues, IssueCodeDuplicateEmail); len(d) != 1 || d[0].Row != 3 || d[0].Field != "email" {
		t.Errorf("duplicate email issues = %+v", d)
	}
	if be := issuesByCode(res.Issues, IssueCodeBadEmail); len(be) != 1 || be[0].Row != 5 || be[0].Severity != SeverityError {
		t.Errorf("bad email issues = %+v", be)
	}
	if mn := issuesByCode(res.Issues, IssueCodeMissingName); len(mn) != 1 || mn[0].Row != 4 {
		t.Errorf("missing name issues = %+v", mn)
	}
	// the CSV-formula defuse prefix is stripped like products
	if strings.HasPrefix(c[4].Name, "'") {
		t.Errorf("name = %q", c[4].Name)
	}
}

func TestParseStock(t *testing.T) {
	file := "SKU,Barcode,Quantity,Sold by weight\n" +
		"A-1,,12,N\n" +
		"A-2,,2.5,N\n" + // decimal, not weighed -> refused
		"A-3,,2.5,Y\n" + // decimal, weighed -> fine
		",,4,N\n" + // no key
		"A-1,,7,N\n" + // duplicate key
		"A-4,,,N\n" + // blank quantity
		"A-5,,-3,N\n" + // negative
		"A-6,1000000000011,1,N\n"
	res, err := ParseStock(strings.NewReader(file), smartOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 8 {
		t.Fatalf("rows = %d", len(res.Rows))
	}
	if res.Rows[0].Quantity != 12 || res.Rows[2].Quantity != 2.5 || res.Rows[7].Barcode != "1000000000011" {
		t.Errorf("rows misread: %+v", res.Rows)
	}
	want := map[string]int{IssueCodeDecimalQuantity: 3, IssueCodeMissingKey: 5, IssueCodeDuplicateKey: 6, IssueCodeMissingQuantity: 7, IssueCodeNegativeQuantity: 8}
	for code, row := range want {
		is := issuesByCode(res.Issues, code)
		if len(is) != 1 || is[0].Row != row || is[0].Severity != SeverityError {
			t.Errorf("%s issues = %+v, want one on row %d", code, is, row)
		}
	}
	if d := issuesByCode(res.Issues, IssueCodeDecimalQuantity); len(d) == 1 && (d[0].Suggested == nil || *d[0].Suggested != "3") {
		t.Errorf("decimal suggestion = %+v", d[0])
	}
	// the caller may know an item is weighed even when the file does not say
	o := smartOpts()
	o.IsWeighed = func(sku, barcode string) bool { return sku == "A-2" }
	res, err = ParseStock(strings.NewReader(file), o)
	if err != nil {
		t.Fatal(err)
	}
	if d := issuesByCode(res.Issues, IssueCodeDecimalQuantity); len(d) != 0 {
		t.Errorf("weighed per caller: %+v", d)
	}
}

func TestParseStockDECommaAndTR(t *testing.T) {
	res, err := ParseStock(strings.NewReader("Artikelnummer;Bestand\r\nX1;1.000\r\nX2;4\r\n"), smartOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 0 || len(res.Rows) != 2 || res.Rows[1].Quantity != 4 {
		t.Fatalf("%+v %+v", res.Rows, res.Issues)
	}
	res, err = ParseStock(strings.NewReader("Stok Kodu;Miktar\r\nX1;5\r\n"), smartOpts())
	if err != nil || len(res.Rows) != 1 || res.Rows[0].SKU != "X1" || res.Rows[0].Quantity != 5 {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestRowForCarriesActiveOnlyWhenTheSourceSaidSo(t *testing.T) {
	r, ok := RowFor(ImportItem{Name: "A", HasActive: true, Active: false})
	if !ok || r.Active == nil || *r.Active {
		t.Fatalf("active=false must be stated: %+v", r.Active)
	}
	r, _ = RowFor(ImportItem{Name: "A"})
	if r.Active != nil {
		t.Fatalf("no Active column, no statement: %v", *r.Active)
	}
}
