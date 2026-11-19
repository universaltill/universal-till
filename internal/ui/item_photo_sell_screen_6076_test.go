package ui

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/iconid"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#6076: the demo seed freezes each item's bundled thumb into
// shortcut_buttons.image_path; clearing the item's photo (cloud clear keeps
// the icon, "No image" clears both) must not leave that frozen path winning
// over the item's own picture on the quick tile.
func TestButtonStore_ClearedDemoPhotoShowsIconNotFrozenThumb_6076(t *testing.T) {
	const frozen = "/public/assets/items/itm001/thumb.png"
	beer := iconid.AssetPath("lucide:beer")
	cases := []struct {
		name  string
		setup func(t *testing.T, cr *data.CatalogRepo)
		item  string
		want  string
	}{
		{"cloud clear keeps icon", func(t *testing.T, cr *data.CatalogRepo) {
			if err := cr.ClearItemPhoto(context.Background(), "itm001"); err != nil {
				t.Fatal(err)
			}
		}, "itm001", beer},
		{"No image blanks", func(t *testing.T, cr *data.CatalogRepo) {
			if err := cr.ClearItemThumbnail(context.Background(), "itm001"); err != nil {
				t.Fatal(err)
			}
		}, "itm001", ""},
		{"never cleared keeps bundled photo", func(t *testing.T, cr *data.CatalogRepo) {}, "itm002", "/public/assets/items/itm002/thumb.png"},
		{"uploaded then cloud-cleared", func(t *testing.T, cr *data.CatalogRepo) {
			if err := cr.SetItemPhoto(context.Background(), "itm003", "/public/assets/items/itm003/upload.png"); err != nil {
				t.Fatal(err)
			}
			if err := cr.ClearItemPhoto(context.Background(), "itm003"); err != nil {
				t.Fatal(err)
			}
		}, "itm003", beer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := db.Open(testsupport.MigratedDBFile(t, "tile6076.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Close() })
			ctx := context.Background()
			if err := data.NewDemoSeedRepo(d.DB).SeedDemoCatalogue(ctx); err != nil {
				t.Fatal(err)
			}
			mustExec(t, d.DB, `UPDATE items SET icon='lucide:beer' WHERE id IN ('itm001','itm003')`)
			tc.setup(t, data.NewCatalogRepo(d.DB))

			store := NewButtonStore(d.DB)
			load := map[string]func() ([]Button, error){
				"Load":     store.Load,
				"LoadWith": func() ([]Button, error) { return store.LoadWith(ctx, nil) },
			}
			for path, fn := range load {
				btns, err := fn()
				if err != nil {
					t.Fatalf("%s: %v", path, err)
				}
				found := false
				for _, b := range btns {
					if b.ItemID != tc.item || !b.QuickButton {
						continue
					}
					found = true
					if b.ImageURL != tc.want {
						t.Errorf("%s: %s ImageURL = %q, want %q (frozen was %s)", path, tc.item, b.ImageURL, tc.want, frozen)
					}
				}
				if !found {
					t.Fatalf("%s: no explicit tile for %s", path, tc.item)
				}
			}
		})
	}
}
