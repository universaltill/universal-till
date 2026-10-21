package catimport

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// ADR-0164 §2 (ut-docs#4526): the header row a file-rows import entry is
// matched against, read the same way on the till and in the cloud.

func TestHeaderRow_CSV(t *testing.T) {
	for name, in := range map[string]string{
		"plain":       "Product Ref,Product Title,Price\n1,Tea,2.00\n",
		"bom":         "\uFEFFProduct Ref,Product Title,Price\r\n1,Tea,2.00\r\n",
		"quoted":      "\"Product Ref\",\"Product Title\",Price\n",
		"bom quoted":  "\uFEFF\"Product Ref\",\"Product Title\",Price\r\n",
		"header only": "Product Ref,Product Title,Price",
	} {
		got, err := HeaderRow(strings.NewReader(in), int64(len(in)), "export.csv")
		if err != nil || strings.Join(got, "|") != "Product Ref|Product Title|Price" {
			t.Errorf("%s: HeaderRow = %q, %v", name, got, err)
		}
	}
	if _, err := HeaderRow(strings.NewReader(""), 0, "export.csv"); err == nil {
		t.Error("empty file: want an error")
	}
}

// A header row longer than the read cap is refused, never read unbounded.
func TestHeaderRow_CSVBounded(t *testing.T) {
	huge := strings.Repeat("a", HeaderRowMaxBytes+10) + "\n"
	if _, err := HeaderRow(strings.NewReader(huge), int64(len(huge)), "export.csv"); !errors.Is(err, ErrHeaderRowTooLong) {
		t.Fatalf("over-long header row: err = %v, want ErrHeaderRowTooLong", err)
	}
}

func TestHeaderRow_XLSX(t *testing.T) {
	raw := buildXLSX(t, [][]string{{"", ""}, {"Product Ref", "Product Title", "Price"}, {"1", "Tea", "2"}})
	got, err := HeaderRow(bytes.NewReader(raw), int64(len(raw)), "export.xlsx")
	if err != nil || strings.Join(got, "|") != "Product Ref|Product Title|Price" {
		t.Fatalf("HeaderRow(xlsx) = %q, %v (want the first non-blank row)", got, err)
	}
}

func TestHeadersClaim(t *testing.T) {
	headers := []string{" Product Ref ", "PRODUCT TITLE", "Price"}
	cases := []struct {
		want []string
		ok   bool
	}{
		{[]string{"product ref", "Product Title"}, true},
		{[]string{"Product Ref", "Cost"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := HeadersClaim(headers, c.want); got != c.ok {
			t.Errorf("HeadersClaim(%q) = %v, want %v", c.want, got, c.ok)
		}
	}
}

// A ZIP is read as a workbook only when the file is named as one: a .csv
// that happens to be a ZIP is not opened with the workbook parser.
func TestHeaderRow_WorkbookOnlyByName(t *testing.T) {
	raw := buildXLSX(t, [][]string{{"Product Ref", "Product Title"}})
	for _, name := range []string{"export.xlsx", "EXPORT.XLSM"} {
		got, err := HeaderRow(bytes.NewReader(raw), int64(len(raw)), name)
		if err != nil || strings.Join(got, "|") != "Product Ref|Product Title" {
			t.Errorf("%s: HeaderRow = %q, %v", name, got, err)
		}
	}
	for _, name := range []string{"export.csv", "export.zip", "export"} {
		if got, err := HeaderRow(bytes.NewReader(raw), int64(len(raw)), name); err == nil {
			t.Errorf("%s: a ZIP read as a header row %q, want an error", name, got)
		}
	}
}

// NeutralHeader: a column the standard importer reads in any catalogue
// file (the columnSynonyms it maps, however decorated) is neutral; a
// format-specific detection column or anything else is not.
func TestNeutralHeader(t *testing.T) {
	for _, h := range []string{"Name", " PRICE ", "SKU", "Barcode", "Category", "Price [GBP]", "Tax rate (%)", "Current Quantity [Main]", "Description"} {
		if !NeutralHeader(h) {
			t.Errorf("NeutralHeader(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"fixture_ref", "Product Ref", "Handle", "Variant SKU", "Item id", "Token", "Variation name", ""} {
		if NeutralHeader(h) {
			t.Errorf("NeutralHeader(%q) = true, want false", h)
		}
	}
}

// ut-docs#4726: Epos Now and German Excel exports separate with ";" (or a
// tab), so the header row is read with the separator the file uses.
func TestHeaderRow_Delimiters(t *testing.T) {
	for name, in := range map[string]string{
		"semicolon":        "Name;CategoryId;SalePriceIncTax\r\nTea;Drinks;2.00\r\n",
		"semicolon bom":    "\uFEFFName;CategoryId;SalePriceIncTax\r\nTea;Drinks;2.00\r\n",
		"semicolon quoted": "\"Name\";\"CategoryId\";\"SalePriceIncTax\"\n",
		"tab":              "Name\tCategoryId\tSalePriceIncTax\nTea\tDrinks\t2.00\n",
		"comma":            "Name,CategoryId,SalePriceIncTax\nTea,Drinks,2.00\n",
	} {
		got, err := HeaderRow(strings.NewReader(in), int64(len(in)), "export.csv")
		if err != nil || strings.Join(got, "|") != "Name|CategoryId|SalePriceIncTax" {
			t.Errorf("%s: HeaderRow = %q, %v", name, got, err)
		}
	}
}

// A separator that only appears inside a quoted field is not the separator.
func TestHeaderRow_DelimiterInQuotes(t *testing.T) {
	in := "Name,\"Tax; rate\",Price\nTea,20,2.00\n"
	got, err := HeaderRow(strings.NewReader(in), int64(len(in)), "export.csv")
	if err != nil || strings.Join(got, "|") != "Name|Tax; rate|Price" {
		t.Fatalf("HeaderRow = %q, %v", got, err)
	}
}

func TestParse_SemicolonAndTab(t *testing.T) {
	for name, in := range map[string]string{
		"semicolon": "Name;Price;SKU\r\nTea;2.00;T-1\r\n",
		"tab":       "Name\tPrice\tSKU\nTea\t2.00\tT-1\n",
	} {
		res, err := Parse(strings.NewReader(in), 2, nil, false)
		if err != nil || len(res.Items) != 1 || res.Items[0].Name != "Tea" || res.Items[0].SKU != "T-1" {
			t.Errorf("%s: Parse = %+v, %v", name, res.Items, err)
		}
	}
}

// A BOM in front of a quoted header, with no ";"/"," tricks: Parse reads it
// as HeaderRow does (review of ut-docs#4726).
func TestParse_BOMBeforeQuotedHeader(t *testing.T) {
	in := "\uFEFF\"Name\";\"Price\";\"SKU\"\r\n\"Tea\";\"2.00\";\"T-1\"\r\n"
	res, err := Parse(strings.NewReader(in), 2, nil, false)
	if err != nil || len(res.Items) != 1 || res.Items[0].Name != "Tea" {
		t.Fatalf("Parse = %+v, %v", res.Items, err)
	}
}
