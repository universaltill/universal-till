package catalog

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/iconid"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/testsupport"
)

type iconResolver map[string]pos.BasketLine

func (m iconResolver) Resolve(code string) (pos.BasketLine, bool) { l, ok := m[code]; return l, ok }

// ut-docs#5124: refreshing an open basket after an item edit/sync must not
// blank a line whose picture is an icon id (no thumbnail row).
func TestRefreshBasketPhoto_IconOnlyItemKeepsIcon(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Icon Item", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`UPDATE items SET icon = 'lucide:beer' WHERE id = 'i1'`); err != nil {
		t.Fatal(err)
	}
	eng := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, iconResolver{
		"S1": {SKU: "S1", Name: "Icon Item", Qty: 1, PriceCents: 100, ItemID: "i1", TaxCodeID: "tax_std"},
	})
	eng.Scan("S1")
	d := &common.Deps{Engine: eng}
	RefreshBasketPhoto(context.Background(), d, data.NewCatalogRepo(db), "i1")
	want := iconid.AssetPath("lucide:beer")
	if got := eng.Basket().Lines[0].ImageURL; want == "" || got != want {
		t.Fatalf("basket line ImageURL = %q, want %q", got, want)
	}
}
