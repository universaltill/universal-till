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

const genericCSV = `Product Name,Retail Price,EAN,Department
Widget,2.00,4006381333931,Bits
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
