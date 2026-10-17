package catsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#4542: description, age_restricted and net_quantity_value/_unit
// are additive ADR-0051 row fields. The source wins like name when a row
// carries one; an absent field leaves the item as it is.

const wine = `{"external_id":"w-1","name":"Red wine","price_minor":999,"description":"Dry red\nfrom Rioja","age_restricted":true,"net_quantity_value":750,"net_quantity_unit":"ml"}`

func itemOf(t *testing.T, sqlDB *sql.DB, ext string) catalogtypes.ItemInput {
	t.Helper()
	it, ok, err := data.NewCatalogRepo(sqlDB).GetItem(context.Background(), linkedItem(t, sqlDB, ext))
	if err != nil || !ok {
		t.Fatalf("item %s: %v %v", ext, ok, err)
	}
	return it
}

func netQty(it catalogtypes.ItemInput) string {
	if it.NetQuantityValue == nil || it.NetQuantityUnit == nil {
		return fmt.Sprintf("%v/%v", it.NetQuantityValue, it.NetQuantityUnit)
	}
	return fmt.Sprintf("%d %s", *it.NetQuantityValue, *it.NetQuantityUnit)
}

func TestRun_CreateCarriesDescriptionAgeAndNetQuantity(t *testing.T) {
	sqlDB := openDB(t)
	wantCounts(t, run(t, sqlDB, true, wine), 1, 0, 0, 0)
	it := itemOf(t, sqlDB, "w-1")
	if it.Description != "Dry red\nfrom Rioja" || !it.AgeRestricted || netQty(it) != "750 ml" {
		t.Fatalf("item = description %q age %v net %s", it.Description, it.AgeRestricted, netQty(it))
	}
	// The same row again changes nothing.
	wantCounts(t, run(t, sqlDB, true, wine), 0, 0, 1, 0)
}

func TestRun_UpdateDescriptionAgeAndNetQuantity(t *testing.T) {
	sqlDB := openDB(t)
	run(t, sqlDB, true, wine)
	changed := `{"external_id":"w-1","name":"Red wine","price_minor":999,"description":"Fruity red","age_restricted":false,"net_quantity_value":1000,"net_quantity_unit":"ml"}`

	// A preview reports the changes and writes nothing.
	res := run(t, sqlDB, false, changed)
	wantCounts(t, res, 0, 1, 0, 0)
	if got := strings.Join(res.Samples[0].Changes, ","); got != "description,age_restricted,net_quantity" {
		t.Fatalf("changes = %q", got)
	}
	if it := itemOf(t, sqlDB, "w-1"); it.Description != "Dry red\nfrom Rioja" || !it.AgeRestricted || netQty(it) != "750 ml" {
		t.Fatalf("preview wrote: %+v", it)
	}

	wantCounts(t, run(t, sqlDB, true, changed), 0, 1, 0, 0)
	it := itemOf(t, sqlDB, "w-1")
	if it.Description != "Fruity red" || it.AgeRestricted || netQty(it) != "1000 ml" {
		t.Fatalf("item = description %q age %v net %s", it.Description, it.AgeRestricted, netQty(it))
	}
	if it.Name != "Red wine" || it.BasePrice != 999 {
		t.Fatalf("other fields moved: %+v", it)
	}
	wantCounts(t, run(t, sqlDB, true, changed), 0, 0, 1, 0)
}

// A row without the fields leaves what the till has: an older plugin (or a
// file without the columns) never wipes a description, an age flag or a
// net quantity the merchant set.
func TestRun_AbsentFieldsLeaveItemAsIs(t *testing.T) {
	sqlDB := openDB(t)
	run(t, sqlDB, true, wine)
	bare := `{"external_id":"w-1","name":"Red wine","price_minor":999}`
	wantCounts(t, run(t, sqlDB, true, bare), 0, 0, 1, 0)
	// Another field's change rewrites the row but keeps these.
	wantCounts(t, run(t, sqlDB, true, `{"external_id":"w-1","name":"Red wine 2","price_minor":999}`), 0, 1, 0, 0)
	it := itemOf(t, sqlDB, "w-1")
	if it.Name != "Red wine 2" || it.Description != "Dry red\nfrom Rioja" || !it.AgeRestricted || netQty(it) != "750 ml" {
		t.Fatalf("item = name %q description %q age %v net %s", it.Name, it.Description, it.AgeRestricted, netQty(it))
	}

	// A new item from a bare row gets the defaults.
	run(t, sqlDB, true, `{"external_id":"b-1","name":"Bread","price_minor":200}`)
	if it := itemOf(t, sqlDB, "b-1"); it.Description != "" || it.AgeRestricted || it.NetQuantityValue != nil || it.NetQuantityUnit != nil {
		t.Fatalf("bare new item = %+v", it)
	}
}

// A bad value is a per-row issue; the rest of the batch still applies.
func TestParseRow_DescriptionAndNetQuantityIssues(t *testing.T) {
	base := `{"external_id":"a","name":"x","price_minor":1,%s}`
	cases := []struct {
		name, field, code string
	}{
		{"too long", fmt.Sprintf(`"description":%q`, strings.Repeat("d", descriptionMaxRunes+1)), IssueDescriptionInvalid},
		{"control char", `"description":"bell\u0007"`, IssueDescriptionInvalid},
		{"bidi override", `"description":"abc‮def"`, IssueDescriptionInvalid},
		{"zero-width space", `"description":"zero\u200bwidth"`, IssueDescriptionInvalid},
		{"value only", `"net_quantity_value":250`, IssueNetQuantityInvalid},
		{"unit only", `"net_quantity_unit":"g"`, IssueNetQuantityInvalid},
		{"zero", `"net_quantity_value":0,"net_quantity_unit":"g"`, IssueNetQuantityInvalid},
		{"negative", `"net_quantity_value":-5,"net_quantity_unit":"g"`, IssueNetQuantityInvalid},
		{"bad unit", `"net_quantity_value":5,"net_quantity_unit":"kg"`, IssueNetQuantityInvalid},
		{"age not bool", `"age_restricted":"yes"`, IssueRowNotObject},
	}
	for _, c := range cases {
		_, _, is := parseRow(json.RawMessage(fmt.Sprintf(base, c.field)))
		if is.Code != c.code {
			t.Errorf("%s: code %q, want %q", c.name, is.Code, c.code)
		}
	}
	ok := []string{
		`"description":"line one\nline two\ttabbed"`,
		`"description":"Kaffee\u00adbohnen, \u200fمرحبا\u200e, می\u200cخواهم"`, // soft hyphen, RLM/LRM, ZWNJ
		fmt.Sprintf(`"description":%q`, strings.Repeat("d", descriptionMaxRunes)),
		`"description":"   "`, // blank = absent
		`"net_quantity_value":6,"net_quantity_unit":"ea"`,
		`"age_restricted":false`,
	}
	for _, f := range ok {
		if _, _, is := parseRow(json.RawMessage(fmt.Sprintf(base, f))); !is.none() {
			t.Errorf("%s: unexpected issue %q", f, is.Code)
		}
	}

	sqlDB := openDB(t)
	res := run(t, sqlDB, true,
		`{"external_id":"a","name":"A","price_minor":1,"net_quantity_value":250}`,
		`{"external_id":"b","name":"B","price_minor":1}`,
	)
	wantCounts(t, res, 1, 0, 0, 1)
	if s := res.Samples[0]; s.IssueCode != IssueNetQuantityInvalid || s.Issue != "net_quantity_value and net_quantity_unit are invalid" {
		t.Errorf("sample = %+v", s)
	}
}

// The module's rows for the till's own export columns land like the file
// import: description, age check and net quantity are carried.
func TestRun_ModuleRowsCarryTillExportColumns(t *testing.T) {
	sqlDB := openDB(t)
	csv := "Handle,SKU,Name,Category,Sold by weight,Price,Description,Age restricted,Net quantity,Net quantity unit\n" +
		"wine,30001,Red wine,Drinks,N,9.99,Dry red,Y,750,ml\n"
	wantCounts(t, run(t, sqlDB, true, moduleRows(t, csv, nil)...), 1, 0, 0, 0)
	it := itemOf(t, sqlDB, "wine/30001")
	if it.Description != "Dry red" || !it.AgeRestricted || netQty(it) != "750 ml" {
		t.Fatalf("item = description %q age %v net %s", it.Description, it.AgeRestricted, netQty(it))
	}
}
