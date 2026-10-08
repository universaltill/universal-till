package data_test

import (
	"context"
	"testing"
)

// ut-docs#3015: the snapshot read carries each item's sell-screen state —
// "hidden" (items.sell_screen_hidden), "removed" (items.sell_screen_removed;
// removed wins when both are set, as in SellScreenStates) or "" when visible.
func TestCatalogSnapshotItemsSellScreen(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	f.exec(t, `UPDATE items SET sell_screen_hidden = 1 WHERE id = 'itm1'`)
	f.exec(t, `UPDATE items SET sell_screen_hidden = 1, sell_screen_removed = 1 WHERE id = 'itm-nocat'`)

	items, err := f.catalog.CatalogSnapshotItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"itm1": "hidden", "itm-nocat": "removed"}
	seen := 0
	for _, it := range items {
		if w, ok := want[it.ID]; ok {
			seen++
			if it.SellScreen != w {
				t.Errorf("%s SellScreen = %q, want %q", it.ID, it.SellScreen, w)
			}
			continue
		}
		if it.SellScreen != "" {
			t.Errorf("%s is visible, got SellScreen %q", it.ID, it.SellScreen)
		}
	}
	if seen != len(want) {
		t.Fatalf("saw %d of %d flagged items in the snapshot", seen, len(want))
	}
}
