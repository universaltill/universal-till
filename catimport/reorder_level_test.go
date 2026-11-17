package catimport

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// ut-docs#2084: a "Reorder level" column sets items.reorder_level.

func parseCSVForReorder(t *testing.T, csv string) []ImportItem {
	t.Helper()
	res, err := Parse(strings.NewReader(csv), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return res.Items
}

func TestParse_ReorderLevelColumn(t *testing.T) {
	cases := []struct {
		cell      string
		wantLevel int
		wantHas   bool
		wantIssue bool
	}{
		{"5", 5, true, false},
		{"0", 0, true, false},
		{"", 0, false, false},
		{"5.0", 5, true, false},
		{"1000000", 1000000, true, false},
		{"-1", 0, false, true},
		{"2.5", 0, false, true},
		{"1000001", 0, false, true},
		{"abc", 0, false, true},
		{"1,200", 0, false, true},
		{"NaN", 0, false, true},
	}
	for _, c := range cases {
		items := parseCSVForReorder(t, "Name,Price,Reorder level\nA,1.00,\""+c.cell+"\"\n")
		if len(items) != 1 {
			t.Fatalf("%q: items = %d", c.cell, len(items))
		}
		it := items[0]
		if it.ReorderLevel != c.wantLevel || it.HasReorderLevel != c.wantHas {
			t.Errorf("%q: level/has = %d/%v, want %d/%v", c.cell, it.ReorderLevel, it.HasReorderLevel, c.wantLevel, c.wantHas)
		}
		if c.wantIssue {
			if it.ReorderLevelIssue != ReorderLevelIssueInvalid || it.ReorderLevelIssueRaw != c.cell {
				t.Errorf("%q: issue/raw = %q/%q", c.cell, it.ReorderLevelIssue, it.ReorderLevelIssueRaw)
			}
		} else if it.ReorderLevelIssue != "" {
			t.Errorf("%q: unexpected issue %q", c.cell, it.ReorderLevelIssue)
		}
		if it.Issue != "" {
			t.Errorf("%q: row must not be blocked, Issue=%q", c.cell, it.Issue)
		}
	}
	// No column at all.
	it := parseCSVForReorder(t, "Name,Price\nA,1.00\n")[0]
	if it.HasReorderLevel || it.ReorderLevel != 0 || it.ReorderLevelIssue != "" {
		t.Errorf("no column: %+v", it)
	}
}

func TestParse_ReorderLevelSourceHeaders(t *testing.T) {
	it := parseCSVForReorder(t, "Handle,Name,Price,In stock [Main],Low stock [Main]\nh1,A,1.00,20,5\n")[0]
	if !it.HasStock || it.Stock != 20 || !it.HasReorderLevel || it.ReorderLevel != 5 {
		t.Errorf("loyverse: stock=%v/%v reorder=%d/%v", it.Stock, it.HasStock, it.ReorderLevel, it.HasReorderLevel)
	}
	it = parseCSVForReorder(t, "Item Name,Price,Current Quantity [Main],Stock Alert Count [Main]\nA,1.00,3,10\n")[0]
	if !it.HasStock || it.Stock != 3 || !it.HasReorderLevel || it.ReorderLevel != 10 {
		t.Errorf("square: stock=%v/%v reorder=%d/%v", it.Stock, it.HasStock, it.ReorderLevel, it.HasReorderLevel)
	}
	it = parseCSVForReorder(t, "Name,Price,Optimal stock [Main]\nA,1.00,9\n")[0]
	if it.HasReorderLevel {
		t.Errorf("Optimal stock must not be a reorder level: %+v", it)
	}
}

func TestParse_ReorderAlertDisabledDropsCount(t *testing.T) {
	hdr := "Name,Price,Stock Alert Enabled [Main],Stock Alert Count [Main]\n"
	it := parseCSVForReorder(t, hdr+"A,1.00,N,10\n")[0]
	if it.HasReorderLevel || it.ReorderLevelIssue != "" {
		t.Errorf("alert N: %+v", it)
	}
	it = parseCSVForReorder(t, hdr+"A,1.00,Y,10\n")[0]
	if !it.HasReorderLevel || it.ReorderLevel != 10 {
		t.Errorf("alert Y: %+v", it)
	}
	it = parseCSVForReorder(t, hdr+"A,1.00,N,abc\n")[0]
	if it.HasReorderLevel || it.ReorderLevelIssue != "" {
		t.Errorf("alert N + junk count: %+v", it)
	}
}

func TestHeaderIndex_ReorderSynonymsDoNotStealStock(t *testing.T) {
	for _, field := range []string{"reorder_level", "reorder_alert"} {
		for _, s := range columnSynonyms[field] {
			for _, other := range []string{"stock", "track_stock"} {
				for _, o := range columnSynonyms[other] {
					if s == o || strings.HasPrefix(s, o+" [") {
						t.Errorf("%s synonym %q collides with %s %q", field, s, other, o)
					}
				}
			}
			idx := headerIndex([]string{"Name", s + " [Main]"})
			if _, ok := idx["stock"]; ok {
				t.Errorf("%q bracket form claimed stock", s)
			}
			if _, ok := idx[field]; !ok {
				t.Errorf("%q bracket form did not claim %s", s, field)
			}
		}
	}
	idx := headerIndex([]string{"Name", "In stock [Main]", "Low stock [Main]"})
	if idx["stock"] != 1 || idx["reorder_level"] != 2 {
		t.Errorf("idx = %v", idx)
	}
}

func TestParseXLSX_ReorderLevelMatchesCSV(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for i, h := range []string{"Name", "Price", "Reorder level"} {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		mustXLSX(t, f.SetCellStr("Sheet1", cell, h))
	}
	fmtID, err := f.NewStyle(&excelize.Style{NumFmt: 3}) // #,##0
	if err != nil {
		t.Fatal(err)
	}
	mustXLSX(t, f.SetCellStr("Sheet1", "A2", "Num"))
	mustXLSX(t, f.SetCellStr("Sheet1", "B2", "1.00"))
	mustXLSX(t, f.SetCellFloat("Sheet1", "C2", 1200, -1, 64))
	mustXLSX(t, f.SetCellStyle("Sheet1", "C2", "C2", fmtID))
	mustXLSX(t, f.SetCellStr("Sheet1", "A3", "Text"))
	mustXLSX(t, f.SetCellStr("Sheet1", "B3", "1.00"))
	mustXLSX(t, f.SetCellStr("Sheet1", "C3", "abc"))
	mustXLSX(t, f.SetCellStr("Sheet1", "A4", "Zero"))
	mustXLSX(t, f.SetCellStr("Sheet1", "B4", "1.00"))
	mustXLSX(t, f.SetCellFloat("Sheet1", "C4", 0, -1, 64))
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	xres, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatal(err)
	}
	cres := parseCSVForReorder(t, "Name,Price,Reorder level\nNum,1.00,1200\nText,1.00,abc\nZero,1.00,0\n")
	if len(xres.Items) != 3 || len(cres) != 3 {
		t.Fatalf("items xlsx=%d csv=%d", len(xres.Items), len(cres))
	}
	for i := range cres {
		x, c := xres.Items[i], cres[i]
		xs := fmt.Sprint(x.ReorderLevel, x.HasReorderLevel, x.ReorderLevelIssue, x.ReorderLevelIssueRaw)
		cs := fmt.Sprint(c.ReorderLevel, c.HasReorderLevel, c.ReorderLevelIssue, c.ReorderLevelIssueRaw)
		if xs != cs {
			t.Errorf("row %d: xlsx %q != csv %q", i, xs, cs)
		}
	}
	if xres.Items[0].ReorderLevel != 1200 || !xres.Items[0].HasReorderLevel {
		t.Errorf("1200 rejected: %+v", xres.Items[0])
	}
}
