package data_test

import (
	"context"
	"strings"
	"testing"
)

// ut-docs#4163: the snapshot read carries each item's applied option sets
// in axis order (inactive sets included — they are still applied), [] for
// an item with none, so my. can show which sets an item's range uses.
func TestCatalogSnapshotItemsOptionSetIDs(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	f.exec(t, `INSERT INTO option_sets (id, name, is_active) VALUES ('set-colour','Colour',1), ('set-size','Size',0)`)
	// Size is axis 0, Colour axis 1: the order is axis_order, not name or id.
	f.exec(t, `INSERT INTO item_option_sets (item_id, option_set_id, axis_order) VALUES ('itm1','set-colour',1), ('itm1','set-size',0)`)

	items, err := f.catalog.CatalogSnapshotItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, it := range items {
		if it.OptionSetIDs == nil {
			t.Fatalf("%s OptionSetIDs is nil; the wire shape is [] never null", it.ID)
		}
		if it.ID == "itm1" {
			seen = true
			if got := strings.Join(it.OptionSetIDs, ","); got != "set-size,set-colour" {
				t.Errorf("itm1 OptionSetIDs = %q, want axis order set-size,set-colour", got)
			}
			continue
		}
		if len(it.OptionSetIDs) != 0 {
			t.Errorf("%s has no applied sets, got %v", it.ID, it.OptionSetIDs)
		}
	}
	if !seen {
		t.Fatal("itm1 missing from the snapshot")
	}
}
