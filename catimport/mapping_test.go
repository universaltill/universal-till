package catimport

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func mappingCode(t *testing.T, err error) string {
	t.Helper()
	var me *MappingError
	if !errors.As(err, &me) {
		t.Fatalf("want *MappingError, got %v", err)
	}
	return me.Code
}

// A generic file whose columns no synonym knows parses once mapped
// (ut-docs#4550); without the mapping it has no name column.
func TestParseMapped_UnknownHeadersMapped(t *testing.T) {
	csv := "Artikel,Preis EUR,Warengruppe\nCola,1.50,Drinks\nTea,2.00,Drinks\n"
	if _, err := Parse(strings.NewReader(csv), 2, nil, false); !errors.Is(err, ErrNoNameColumn) {
		t.Fatalf("unmapped: want ErrNoNameColumn, got %v", err)
	}
	m := Mapping{"name": "artikel", "price": " Preis  EUR ", "category": "Warengruppe"}
	res, err := ParseMapped(strings.NewReader(csv), m, 2, nil, false)
	if err != nil {
		t.Fatalf("ParseMapped: %v", err)
	}
	if res.Format != FormatGeneric || len(res.Items) != 2 {
		t.Fatalf("got format %q, %d items", res.Format, len(res.Items))
	}
	if it := res.Items[0]; it.Name != "Cola" || it.PriceMinor != 150 || it.Category != "Drinks" || it.Issue != "" {
		t.Fatalf("item 0 = %+v", it)
	}
}

// A mapped column feeds only its mapped field: "Title" is a name synonym,
// but mapped to description it stops being the name.
func TestParseMapped_MappedColumnLeavesItsSynonymField(t *testing.T) {
	csv := "Title,Product,Price\nCold and fizzy,Cola,1.50\n"
	res, err := ParseMapped(strings.NewReader(csv), Mapping{"description": "Title", "name": "Product"}, 2, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if it := res.Items[0]; it.Name != "Cola" || it.Description != "Cold and fizzy" {
		t.Fatalf("item = %+v", it)
	}
	// Mapping only the description away leaves no name column at all.
	if _, err := ParseMapped(strings.NewReader(csv), Mapping{"description": "Title"}, 2, nil, false); !errors.Is(err, ErrNoNameColumn) {
		t.Fatalf("want ErrNoNameColumn, got %v", err)
	}
}

// An empty header unmaps a field the synonyms matched.
func TestParseMapped_EmptyHeaderDropsField(t *testing.T) {
	csv := "Name,Price,Stock\nCola,1.50,12\n"
	res, err := ParseMapped(strings.NewReader(csv), Mapping{"stock": ""}, 2, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].HasStock {
		t.Fatalf("stock read despite the mapping: %+v", res.Items[0])
	}
}

func TestParseMapped_NilMappingIsParse(t *testing.T) {
	csv := "\uFEFFName,Price,SKU\nCola,1.50,C1\n"
	a, errA := Parse(strings.NewReader(csv), 2, nil, false)
	b, errB := ParseMapped(strings.NewReader(csv), Mapping{}, 2, nil, false)
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	if a.Format != b.Format || len(a.Items) != 1 || !reflect.DeepEqual(a, b) {
		t.Fatalf("Parse %+v vs ParseMapped %+v", a, b)
	}
}

func TestValidateMapping(t *testing.T) {
	generic := []string{"\uFEFFArtikel", "Preis", "Notiz", "notiz"}
	cases := []struct {
		name    string
		headers []string
		m       Mapping
		code    string
	}{
		{"ok", generic, Mapping{"name": "Artikel", "price": "preis"}, ""},
		{"empty header", generic, Mapping{"stock": ""}, ""},
		{"unknown field", generic, Mapping{"zz": "Artikel", "aa": "Preis"}, MappingIssueUnknownField},
		{"format-only field", generic, Mapping{"token": "Artikel"}, MappingIssueUnknownField},
		{"header not found", generic, Mapping{"name": "Bezeichnung"}, MappingIssueHeaderNotFound},
		{"header ambiguous", generic, Mapping{"description": "Notiz"}, MappingIssueHeaderAmbiguous},
		{"header reused", generic, Mapping{"name": "Artikel", "description": "artikel"}, MappingIssueHeaderReused},
		{"known export", []string{"Token", "Item Name", "Variation Name", "Price"}, Mapping{"name": "Item Name"}, MappingIssueNotGeneric},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateMapping(c.headers, c.m)
			if c.code == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if got := mappingCode(t, err); got != c.code {
				t.Fatalf("code = %q, want %q", got, c.code)
			}
		})
	}
	// The first unknown field is reported, in sorted order, every time.
	var me *MappingError
	if err := ValidateMapping(generic, Mapping{"zz": "Artikel", "aa": "Preis"}); !errors.As(err, &me) || me.Field != "aa" {
		t.Fatalf("want field aa, got %v", err)
	}
}

// A known export refuses a mapping instead of re-reading its fixed layout.
func TestParseMapped_KnownExportRefused(t *testing.T) {
	csv := "Token,Item Name,Variation Name,Price\nT1,Cola,Regular,1.50\n"
	_, err := ParseMapped(strings.NewReader(csv), Mapping{"name": "Item Name"}, 2, nil, false)
	if got := mappingCode(t, err); got != MappingIssueNotGeneric {
		t.Fatalf("code = %q", got)
	}
}

func TestParseXLSXMapped(t *testing.T) {
	data := buildXLSX(t, [][]string{
		{"Artikel", "Preis EUR"},
		{"Cola", "1.50"},
	})
	if _, err := ParseXLSX(bytes.NewReader(data), int64(len(data)), 2, nil, false); !errors.Is(err, ErrNoNameColumn) {
		t.Fatalf("unmapped: want ErrNoNameColumn, got %v", err)
	}
	res, err := ParseXLSXMapped(bytes.NewReader(data), int64(len(data)), Mapping{"name": "Artikel", "price": "Preis EUR"}, 2, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Name != "Cola" || res.Items[0].PriceMinor != 150 {
		t.Fatalf("items = %+v", res.Items)
	}
	if _, err := ParseXLSXMapped(bytes.NewReader(data), int64(len(data)), Mapping{"name": "Nope"}, 2, nil, false); mappingCode(t, err) != MappingIssueHeaderNotFound {
		t.Fatalf("want header_not_found, got %v", err)
	}
}

// The merged-cell guard covers a mapped column the synonyms never knew.
func TestParseXLSXMapped_MergeOverMappedColumnRejected(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for cell, v := range map[string]string{
		"A1": "Artikel", "B1": "Preis", "C1": "Lager",
		"A2": "Cola", "B2": "1.50", "C2": "4",
		"A3": "Tea", "B3": "2.00", "C3": "",
	} {
		if err := f.SetCellStr("Sheet1", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.MergeCell("Sheet1", "C2", "C3"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	m := Mapping{"name": "Artikel", "price": "Preis"}
	if _, err := ParseXLSXMapped(bytes.NewReader(data), int64(len(data)), m, 2, nil, false); err != nil {
		t.Fatalf("unmapped merge column: want success, got %v", err)
	}
	m["stock"] = "Lager"
	if _, err := ParseXLSXMapped(bytes.NewReader(data), int64(len(data)), m, 2, nil, false); !errors.Is(err, ErrXLSXMergedCells) {
		t.Fatalf("mapped merge column: want ErrXLSXMergedCells, got %v", err)
	}
}

func TestMappableFieldsAreNeutral(t *testing.T) {
	for _, f := range MappableFields() {
		if _, ok := columnSynonyms[f]; !ok || formatOnlyFields[f] {
			t.Errorf("%q is not a neutral columnSynonyms field", f)
		}
	}
	// Every neutral field is mappable, so a new one can't be forgotten.
	mappable := map[string]bool{}
	for _, f := range MappableFields() {
		mappable[f] = true
	}
	for f := range columnSynonyms {
		if !formatOnlyFields[f] && !mappable[f] && f != "shopify_type" {
			t.Errorf("neutral field %q is missing from MappableFields", f)
		}
	}
}
