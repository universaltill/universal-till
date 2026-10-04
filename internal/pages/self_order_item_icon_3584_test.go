package pages

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/iconid"
)

// ut-docs#3584: the self-order kiosk grid shows an icon-only item's icon.
func TestLoadShopItems_IconOnlyItemShowsItsIcon(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-icon", "S1", "5000001", "Lager", 450)
	if _, err := d.DB.Exec(`UPDATE items SET icon = 'lucide:beer' WHERE id = 'itm-icon'`); err != nil {
		t.Fatal(err)
	}
	items, err := loadShopItems(context.Background(), dp)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ItemID == "itm-icon" {
			if it.ImageURL != iconid.AssetPath("lucide:beer") {
				t.Fatalf("kiosk tile ImageURL = %q, want the icon's tile", it.ImageURL)
			}
			return
		}
	}
	t.Fatal("icon item missing from the kiosk grid")
}
