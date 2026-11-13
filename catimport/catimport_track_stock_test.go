package catimport

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// ut-docs#1843. The German pilot merchant's real SumUp export carries
// "Track inventory? (Yes/No)" = No for all 116 items and Quantity = 0 for all
// 116. Before this card the column had no synonym and no field, so the till
// imported 116 items as "stocked, zero on hand" when the source had
// explicitly said the quantity was meaningless.
func TestParse_TrackInventoryColumn(t *testing.T) {
	t.Run("SumUp's parenthesised header is recognised and No is preserved", func(t *testing.T) {
		csv := "Item name,Price,Quantity,Track inventory? (Yes/No)\nCroissant,2.50,0,No\n"
		res, err := Parse(strings.NewReader(csv), 2, testEnabledIDs, false)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(res.Items) != 1 {
			t.Fatalf("got %d items, want 1", len(res.Items))
		}
		it := res.Items[0]
		if !it.HasTracksStock {
			t.Fatal("HasTracksStock is false — the column was not recognised at all")
		}
		if it.TracksStock {
			t.Fatal("TracksStock is true, but the file said No")
		}
	})

	t.Run("Yes is preserved", func(t *testing.T) {
		csv := "Item name,Price,Quantity,Track inventory? (Yes/No)\nCroissant,2.50,7,Yes\n"
		res, err := Parse(strings.NewReader(csv), 2, testEnabledIDs, false)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		it := res.Items[0]
		if !it.HasTracksStock || !it.TracksStock {
			t.Fatalf("HasTracksStock=%v TracksStock=%v, want both true", it.HasTracksStock, it.TracksStock)
		}
		if !it.HasStock || it.Stock != 7 {
			t.Fatalf("HasStock=%v Stock=%v, want true/7 — a tracked item still carries its opening stock", it.HasStock, it.Stock)
		}
	})

	// The distinction the whole change rests on: "the column said No" and
	// "there was no column" must not collapse into the same false.
	t.Run("no column at all leaves both false, and nothing is inferred", func(t *testing.T) {
		csv := "Item name,Price,Quantity\nCroissant,2.50,7\n"
		res, err := Parse(strings.NewReader(csv), 2, testEnabledIDs, false)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		it := res.Items[0]
		if it.HasTracksStock {
			t.Fatal("HasTracksStock is true for a file with no such column")
		}
		if !it.HasStock || it.Stock != 7 {
			t.Fatalf("a file with no tracking column must keep its stock unchanged: HasStock=%v Stock=%v", it.HasStock, it.Stock)
		}
	})

	t.Run("an unrecognised value is not read as No", func(t *testing.T) {
		csv := "Item name,Price,Quantity,Track inventory\nCroissant,2.50,7,maybe\n"
		res, err := Parse(strings.NewReader(csv), 2, testEnabledIDs, false)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if res.Items[0].HasTracksStock {
			t.Fatal("an unparseable value was accepted as an answer")
		}
	})
}

// ut-docs#5957: ParseXLSX(Mapped) read no track_stock column at all, so a
// workbook's "Track inventory?" answer was silently dropped while the same
// rows as CSV kept it. Both paths now share applyTrackStock; this pins them
// row for row, including a real boolean cell (Excel's own TRUE/FALSE) and an
// unrecognised value, which must stay "not said" rather than "No".
func TestParseXLSX_TrackInventoryMatchesCSV(t *testing.T) {
	csv := "Item name,Price,Quantity,Track inventory? (Yes/No)\n" +
		"Croissant,2.50,0,No\n" +
		"Baguette,1.20,7,Yes\n" +
		"Scone,1.00,3,maybe\n" +
		"Muffin,1.50,4,\n"
	want, err := Parse(strings.NewReader(csv), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	f := excelize.NewFile()
	defer f.Close()
	for r, row := range [][]string{
		{"Item name", "Price", "Quantity", "Track inventory? (Yes/No)"},
		{"Croissant", "2.50", "0", "No"},
		{"Baguette", "1.20", "7", "Yes"},
		{"Scone", "1.00", "3", "maybe"},
		{"Muffin", "1.50", "4", ""},
	} {
		for c, v := range row {
			cell, cerr := excelize.CoordinatesToCellName(c+1, r+1)
			mustXLSX(t, cerr)
			mustXLSX(t, f.SetCellStr("Sheet1", cell, v))
		}
	}
	// Two more rows whose answer is a boolean-typed cell, as a workbook
	// saved from Excel with a TRUE/FALSE column carries it.
	for _, r := range []struct {
		row  int
		name string
		b    bool
	}{{6, "Bagel", true}, {7, "Pretzel", false}} {
		mustXLSX(t, f.SetCellStr("Sheet1", fmt.Sprintf("A%d", r.row), r.name))
		mustXLSX(t, f.SetCellStr("Sheet1", fmt.Sprintf("B%d", r.row), "1.00"))
		mustXLSX(t, f.SetCellStr("Sheet1", fmt.Sprintf("C%d", r.row), "2"))
		mustXLSX(t, f.SetCellBool("Sheet1", fmt.Sprintf("D%d", r.row), r.b))
	}
	var buf bytes.Buffer
	if _, werr := f.WriteTo(&buf); werr != nil {
		t.Fatalf("WriteTo: %v", werr)
	}
	got, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v", err)
	}
	if len(got.Items) != 6 {
		t.Fatalf("ParseXLSX: got %d items, want 6", len(got.Items))
	}
	for i, w := range want.Items {
		g := got.Items[i]
		if g.Name != w.Name || g.HasTracksStock != w.HasTracksStock || g.TracksStock != w.TracksStock {
			t.Errorf("row %d: XLSX %s HasTracksStock=%v TracksStock=%v, CSV %s %v/%v",
				i+1, g.Name, g.HasTracksStock, g.TracksStock, w.Name, w.HasTracksStock, w.TracksStock)
		}
	}
	if g := got.Items[4]; !g.HasTracksStock || !g.TracksStock {
		t.Errorf("Bagel (TRUE cell): HasTracksStock=%v TracksStock=%v, want true/true", g.HasTracksStock, g.TracksStock)
	}
	if g := got.Items[5]; !g.HasTracksStock || g.TracksStock {
		t.Errorf("Pretzel (FALSE cell): HasTracksStock=%v TracksStock=%v, want true/false", g.HasTracksStock, g.TracksStock)
	}
}
