package ui

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#4838: a photo set in the item editor (CatalogRepo.SetItemThumbnail,
// item_images role=thumbnail) must show on the item's quick tile whatever the
// tile's own shortcut_buttons.image_path holds. A quick tile whose row froze a
// library/placeholder tile used to keep it over the real photo.
func TestButtonStore_ItemPhotoBeatsFrozenTileImage_4838(t *testing.T) {
	const photo = "/public/assets/items/i1/thumb.png"
	const frozen = "/public/assets/category-icons/generic.svg"
	for name, frozenPath := range map[string]any{"frozen placeholder": frozen, "null": nil} {
		t.Run(name, func(t *testing.T) {
			db := setupFullTestDB(t)
			defer db.Close()
			mustExec(t, db, `CREATE UNIQUE INDEX IF NOT EXISTS ux_item_images_thumbnail_once ON item_images (item_id, role)`)
			mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('i1','S1','Lager', 450, 1)`)
			mustExec(t, db, `INSERT INTO shortcut_buttons(barcode, label, item_id, image_path, sort_order) VALUES('S1','Lager','i1',?,0)`, frozenPath)
			if err := data.NewCatalogRepo(db).SetItemThumbnail(context.Background(), "i1", photo); err != nil {
				t.Fatal(err)
			}
			store := NewButtonStore(db)
			btns, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, b := range btns {
				if b.ItemID == "i1" {
					got = append(got, b.ImageURL)
				}
			}
			if len(got) != 1 || got[0] != photo {
				t.Fatalf("quick tile images = %v, want [%s]", got, photo)
			}
		})
	}
}
