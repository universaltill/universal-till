package catalog

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/iconid"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#3584: an item given an icon from my. (items.icon, no thumbnail
// row) shows that icon on the till's own catalog grid, and the till's
// image tab preselects it as the item's built-in icon.
func TestCatalogPage_IconOnlyItemShowsItsIcon(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Lager", BasePrice: 450, IsActive: true})
	if _, err := db.Exec(`UPDATE items SET icon = 'lucide:beer' WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	asset := iconid.AssetPath("lucide:beer")

	rec := get(t, mux, "/catalog")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d", rec.Code)
	}
	// Scoped to the item's own card: the page's icon picker lists every
	// library tile too.
	page := rec.Body.String()
	start := strings.Index(page, `id="catalog-row-itm1"`)
	if start == -1 {
		t.Fatalf("no catalog-row-itm1 card:\n%s", page)
	}
	card := page[start:]
	card = card[:strings.Index(card, "</button>")]
	if !strings.Contains(card, `src="`+asset) {
		t.Fatalf("the icon-only item's card has no %s tile: %s", asset, card)
	}

	rec = get(t, mux, "/api/catalog/item/icon-state?item_id=itm1")
	var body iconStateBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if body.Data.SelectedKey != "beer" || body.Data.IsCustom || !strings.HasPrefix(body.Data.ThumbnailURL, asset) {
		t.Fatalf("icon-state = %+v, want beer preselected with its tile previewed", body.Data)
	}
}
