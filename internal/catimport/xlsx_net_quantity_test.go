package catimport

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/xuri/excelize/v2"
)

// The card's real scenario: a spreadsheet app re-saving the CSV export
// stores "250" as a NUMERIC cell, not text (buildXLSX only writes text),
// and an operator may then apply a display format. The value must be read
// from the raw cell, like price/stock: a rounding "0" format must not turn
// an invalid 250.4 into a silently-accepted 250, and a grouping/decimal
// format must not get a valid 1000 or 250 rejected as "1,000"/"250.00".
func TestParseXLSX_NetQuantityNumericCellsIgnoreDisplayFormat(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for i, h := range []string{"Name", "Price", "Net quantity", "Net quantity unit"} {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellStr("Sheet1", cell, h); err != nil {
			t.Fatalf("SetCellStr: %v", err)
		}
	}
	style := func(numFmt int) int {
		id, err := f.NewStyle(&excelize.Style{NumFmt: numFmt})
		if err != nil {
			t.Fatalf("NewStyle: %v", err)
		}
		return id
	}
	cases := []struct {
		value     float64
		style     int // 0 = General
		wantValue int64
		wantIssue bool
	}{
		{250, 0, 250, false},
		{1000, style(3), 1000, false}, // "#,##0" displays "1,000"
		{250, style(2), 250, false},   // "0.00" displays "250.00"
		{250.4, style(1), 0, true},    // "0" displays "250"
	}
	for i, c := range cases {
		r := i + 2
		mustXLSX(t, f.SetCellStr("Sheet1", fmt.Sprintf("A%d", r), fmt.Sprintf("Item %d", i)))
		mustXLSX(t, f.SetCellStr("Sheet1", fmt.Sprintf("B%d", r), "1.00"))
		mustXLSX(t, f.SetCellFloat("Sheet1", fmt.Sprintf("C%d", r), c.value, -1, 64))
		if c.style != 0 {
			mustXLSX(t, f.SetCellStyle("Sheet1", fmt.Sprintf("C%d", r), fmt.Sprintf("C%d", r), c.style))
		}
		mustXLSX(t, f.SetCellStr("Sheet1", fmt.Sprintf("D%d", r), "g"))
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	res, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v", err)
	}
	if len(res.Items) != len(cases) {
		t.Fatalf("items = %d, want %d", len(res.Items), len(cases))
	}
	for i, c := range cases {
		it := res.Items[i]
		if c.wantIssue {
			if it.NetQuantityValue != nil || it.NetQuantityIssue != NetQuantityIssueInvalid {
				t.Errorf("case %d (%v): want rejected, got value=%v issue=%q", i, c.value, it.NetQuantityValue, it.NetQuantityIssue)
			}
			continue
		}
		if it.NetQuantityValue == nil || *it.NetQuantityValue != c.wantValue || it.NetQuantityIssue != "" {
			t.Errorf("case %d (%v): want %d g, got value=%v issue=%q raw=%q", i, c.value, c.wantValue, it.NetQuantityValue, it.NetQuantityIssue, it.NetQuantityIssueRaw)
		}
	}
}

func mustXLSX(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("excelize: %v", err)
	}
}

// ut-docs#3473 (follow-up to ut-docs#3403): ParseXLSX must recognise the
// same "Net quantity"/"Net quantity unit" columns the CSV path does
// (TestParseNetQuantityColumns in net_quantity_test.go) — otherwise a
// merchant who re-saves this till's own CSV export as .xlsx and re-uploads
// it loses any configured net quantity silently.
func TestParseXLSX_NetQuantityColumns(t *testing.T) {
	data := buildXLSX(t, [][]string{
		{"Name", "Price", "Net quantity", "Net quantity unit"},
		{"Coffee Beans", "6.50", "250", "g"},
		{"Olive Oil", "8.99", "750", "ML"},
		{"Eggs x6", "2.10", "6", "ea"},
		{"Loose Item", "1.00", "", ""},
		{"Bad Unit", "1.00", "500", "kg"},
	})
	res, err := ParseXLSX(bytes.NewReader(data), int64(len(data)), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v", err)
	}
	if len(res.Items) != 5 {
		t.Fatalf("items = %d, want 5", len(res.Items))
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

	bad := res.Items[4]
	if bad.NetQuantityValue != nil || bad.NetQuantityUnit != nil {
		t.Errorf("invalid net quantity must not be kept (even half of it): %+v", bad)
	}
	if bad.NetQuantityIssue != NetQuantityIssueInvalid || bad.NetQuantityIssueRaw != "500 kg" {
		t.Errorf("NetQuantityIssue/Raw = %q/%q, want %q/%q", bad.NetQuantityIssue, bad.NetQuantityIssueRaw, NetQuantityIssueInvalid, "500 kg")
	}
	if bad.Issue != "" {
		t.Errorf("a bad net quantity must never block the row, got Issue %q", bad.Issue)
	}
}

// A workbook with no net-quantity columns at all (every pre-existing xlsx
// fixture) must leave the fields untouched, same as the CSV path.
func TestParseXLSX_NoNetQuantityColumnLeavesItUnset(t *testing.T) {
	data := buildXLSX(t, [][]string{
		{"Name", "Price"},
		{"Widget", "1.00"},
	})
	res, err := ParseXLSX(bytes.NewReader(data), int64(len(data)), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	it := res.Items[0]
	if it.NetQuantityValue != nil || it.NetQuantityUnit != nil || it.NetQuantityIssue != "" {
		t.Errorf("net quantity must stay unset without its columns: %+v", it)
	}
}
