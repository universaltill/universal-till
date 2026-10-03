package ui

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/iconid"
)

// ut-docs#3584: an item with no image draws its icon id through the till's
// icon registry, the same as a category tile (CategoryThumb, ut-docs#2717).
// A photo wins; an unknown id draws the neutral fallback, never the raw
// value; no icon and no image is "" (the tile's colour swatch).
func TestItemThumb(t *testing.T) {
	const photo = "/public/assets/items/i1/thumb.png"
	for _, c := range []struct {
		path, icon, want string
	}{
		{"", "lucide:beer", iconid.AssetPath("lucide:beer")},
		{photo, "", photo},
		{photo, "lucide:beer", photo},
		{"", "lucide:not-in-this-till", iconid.AssetPath(iconid.Fallback)},
		{"", "<svg onload=x>", iconid.AssetPath(iconid.Fallback)},
		{"", "", ""},
	} {
		if got := ItemThumb(c.path, c.icon); got != c.want {
			t.Errorf("ItemThumb(%q, %q) = %q, want %q", c.path, c.icon, got, c.want)
		}
	}
	if iconid.AssetPath("lucide:beer") == "" {
		t.Fatal("lucide:beer must be in the registry for this test to mean anything")
	}
}

// The sale-screen grid (LoadAllActive, which also feeds the implicit quick
// buttons) and the sell-screen search draw an icon-only item's icon tile.
func TestButtonStore_IconOnlyItemTile(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, icon) VALUES('i1','S1','Lager', 450, 1, 'lucide:beer')`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i2','S2','Plain', 100, 1)`)
	want := iconid.AssetPath("lucide:beer")

	store := NewButtonStore(db)
	all, err := store.LoadAllActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range all {
		got[b.ItemID] = b.ImageURL
	}
	if got["i1"] != want || got["i2"] != "" {
		t.Fatalf("LoadAllActive tiles = %v, want i1 → %s and i2 without a picture", got, want)
	}

	found, err := store.SearchSellable(context.Background(), "Lager", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ImageURL != want {
		t.Fatalf("SearchSellable = %+v, want the icon tile", found)
	}
}
