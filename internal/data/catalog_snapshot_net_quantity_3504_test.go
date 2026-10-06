package data_test

import (
	"context"
	"testing"
)

// ut-docs#3504: the snapshot read carries items.net_quantity_value/unit
// (migration 060, ut-docs#3391) so the push can send them to my.; both nil
// when the item has none.
func TestCatalogSnapshotItemsNetQuantity(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	f.exec(t, `UPDATE items SET net_quantity_value = 500, net_quantity_unit = 'g' WHERE id = 'itm1'`)

	items, err := f.catalog.CatalogSnapshotItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, it := range items {
		if it.ID == "itm1" {
			seen = true
			if it.NetQuantityValue == nil || *it.NetQuantityValue != 500 || it.NetQuantityUnit == nil || *it.NetQuantityUnit != "g" {
				t.Fatalf("itm1 net quantity = %v %v, want 500 g", it.NetQuantityValue, it.NetQuantityUnit)
			}
			continue
		}
		if it.NetQuantityValue != nil || it.NetQuantityUnit != nil {
			t.Fatalf("%s has no net quantity, got %v %v", it.ID, it.NetQuantityValue, it.NetQuantityUnit)
		}
	}
	if !seen {
		t.Fatal("itm1 missing from snapshot")
	}
}
