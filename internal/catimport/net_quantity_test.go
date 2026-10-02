package catimport

import (
	"strings"
	"testing"
)

// ut-docs#3403: the "Net quantity"/"Net quantity unit" columns (the exact
// headers this till's own catalog export writes) carry a pre-packed item's
// net content. A valid pair comes through as the pointer pair
// catalogtypes.ItemInput stores; blank cells mean none; anything that fails
// catalogtypes.ValidNetQuantity is reported via NetQuantityIssue and never
// kept half-set — and never blocks the row, same as a bad tax cell.
const netQuantityCSV = `Name,Price,Net quantity,Net quantity unit
Coffee Beans,6.50,250,g
Olive Oil,8.99,750,ML
Eggs x6,2.10,6,ea
Loose Item,1.00,,
Bad Unit,1.00,500,kg
Zero Value,1.00,0,g
Negative Value,1.00,-5,ml
Value Only,1.00,250,
Unit Only,1.00,,g
Fractional,1.00,1.5,g
`

func TestParseNetQuantityColumns(t *testing.T) {
	res, err := Parse(strings.NewReader(netQuantityCSV), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Items) != 10 {
		t.Fatalf("items = %d, want 10", len(res.Items))
	}

	valid := []struct {
		row   int
		value int64
		unit  string
	}{
		{0, 250, "g"},
		{1, 750, "ml"}, // unit matched case-insensitively, stored lower-case
		{2, 6, "ea"},
	}
	for _, c := range valid {
		it := res.Items[c.row]
		if it.NetQuantityValue == nil || it.NetQuantityUnit == nil {
			t.Errorf("%s: net quantity not parsed: %+v", it.Name, it)
			continue
		}
		if *it.NetQuantityValue != c.value || *it.NetQuantityUnit != c.unit {
			t.Errorf("%s: net quantity = %d %q, want %d %q", it.Name, *it.NetQuantityValue, *it.NetQuantityUnit, c.value, c.unit)
		}
		if it.NetQuantityIssue != "" {
			t.Errorf("%s: valid net quantity must carry no issue, got %q", it.Name, it.NetQuantityIssue)
		}
	}

	loose := res.Items[3]
	if loose.NetQuantityValue != nil || loose.NetQuantityUnit != nil || loose.NetQuantityIssue != "" {
		t.Errorf("blank net quantity cells must mean none, silently: %+v", loose)
	}

	invalid := []struct {
		row int
		raw string
	}{
		{4, "500 kg"},
		{5, "0 g"},
		{6, "-5 ml"},
		{7, "250"},
		{8, "g"},
		{9, "1.5 g"},
	}
	for _, c := range invalid {
		it := res.Items[c.row]
		if it.NetQuantityValue != nil || it.NetQuantityUnit != nil {
			t.Errorf("%s: invalid net quantity must not be kept (even half of it): %+v", it.Name, it)
		}
		if it.NetQuantityIssue != NetQuantityIssueInvalid || it.NetQuantityIssueRaw != c.raw {
			t.Errorf("%s: NetQuantityIssue/Raw = %q/%q, want %q/%q", it.Name, it.NetQuantityIssue, it.NetQuantityIssueRaw, NetQuantityIssueInvalid, c.raw)
		}
		if it.Issue != "" {
			t.Errorf("%s: a bad net quantity must never block the row, got Issue %q", it.Name, it.Issue)
		}
	}
}

// A file with no net-quantity columns at all (every pre-existing fixture)
// leaves the fields untouched — and a plain "Quantity" column stays stock,
// never mistaken for a net quantity.
func TestParseNoNetQuantityColumnLeavesItUnset(t *testing.T) {
	res, err := Parse(strings.NewReader("Name,Price,Quantity\nWidget,1.00,12\n"), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	it := res.Items[0]
	if it.NetQuantityValue != nil || it.NetQuantityUnit != nil || it.NetQuantityIssue != "" {
		t.Errorf("net quantity must stay unset without its columns: %+v", it)
	}
	if !it.HasStock || it.Stock != 12 {
		t.Errorf("\"Quantity\" must still read as stock: %+v", it)
	}
}
