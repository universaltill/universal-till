package pages

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/iconid"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#5124: resuming a held sale re-reads each line's picture; an
// item whose picture is an icon id (set on my., no thumbnail row) must keep
// showing it instead of blanking.
func TestRefreshHeldLinePhotos_IconOnlyItem(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Icon Item", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`UPDATE items SET icon = 'lucide:beer' WHERE id = 'i1'`); err != nil {
		t.Fatal(err)
	}
	snap := &pos.BasketSnapshot{Lines: []pos.SnapshotLine{{ItemID: "i1"}, {ItemID: ""}}}
	refreshHeldLinePhotos(context.Background(), data.NewCatalogRepo(db), snap)
	if want := iconid.AssetPath("lucide:beer"); want == "" || snap.Lines[0].ImageURL != want {
		t.Fatalf("held line ImageURL = %q, want %q", snap.Lines[0].ImageURL, want)
	}
}
