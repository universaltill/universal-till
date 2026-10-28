package pages

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/iconid"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/testsupport"
)

type iconLineResolver map[string]pos.BasketLine

func (m iconLineResolver) Resolve(code string) (pos.BasketLine, bool) { l, ok := m[code]; return l, ok }

// ut-docs#5124: an icon set on my. (save_item directive) reaches the item
// AND the already-open basket line, with no re-ring.
func TestCloudSaveItem_IconReachesOpenBasketLine(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Icon Item", BasePrice: 100, IsActive: true})
	eng := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, iconLineResolver{
		"S1": {SKU: "S1", Name: "Icon Item", Qty: 1, PriceCents: 100, ItemID: "i1", TaxCodeID: "tax_std"},
	})
	eng.Scan("S1")
	if got := eng.Basket().Lines[0].ImageURL; got != "" {
		t.Fatalf("precondition: line already has picture %q", got)
	}
	icon := "lucide:beer"
	d := &common.Deps{Db: db, Engine: eng}
	if _, err := cloudSaveItem(context.Background(), d, data.ItemPatch{ID: "i1", Icon: &icon}); err != nil {
		t.Fatal(err)
	}
	if want := iconid.AssetPath(icon); want == "" || eng.Basket().Lines[0].ImageURL != want {
		t.Fatalf("basket line ImageURL = %q, want %q", eng.Basket().Lines[0].ImageURL, want)
	}
}
