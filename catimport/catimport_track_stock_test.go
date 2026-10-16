package catimport

import (
	"strings"
	"testing"
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
