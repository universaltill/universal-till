package catalog

// ut-docs#1842 (originally): the catalog list must render an item's REAL
// thumbnail (item_images, role=thumbnail) instead of guessing a
// <id>/thumb.png file path. ut-docs#1951 reshaped the list from a table
// into a card grid — every card independently renders its own
// thumbnail-or-color-or-blank slot (no shared column to add/remove across
// rows), which retired the column-toggle/colspan machinery these tests
// used to pin. What's still real and still worth a regression test: a
// card with an image shows it, a card without one falls back to its color
// tile (or nothing), and the OOB fragment path carries the same fix as
// the initial page load.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func TestCatalogPage_CardWithNoImageHasNoThumbTag(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "plain1", SKU: "P1", Name: "Plain Item", BasePrice: 100, IsActive: true})

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="catalog-row-plain1"`) {
		t.Fatalf("expected the item's card, got:\n%s", body)
	}
	// No <img> for a card with no thumbnail — it falls back to its color
	// tile (or nothing) instead of a placeholder image tag.
	if strings.Contains(body, `img class="thumb"`) {
		t.Fatalf("expected no thumbnail <img> for an imageless item, got:\n%s", body)
	}
}

func TestCatalogPage_CardWithImageShowsRealThumbnail(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "imaged1", SKU: "I1", Name: "Imaged Item", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "plain2", SKU: "P2", Name: "Plain Item Two", BasePrice: 100, IsActive: true})
	testsupport.SeedImage(t, db, "img-1", "imaged1", "/public/assets/items/imaged1/thumb.png")

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/public/assets/items/imaged1/thumb.png") {
		t.Fatalf("expected the imaged item's real thumbnail path rendered, got:\n%s", body)
	}
	// The imageless sibling card must NOT pick up the other item's image or
	// otherwise render a thumbnail tag it has no thumbnail for — exactly
	// one <img class="thumb"> on the page, imaged1's own.
	if !strings.Contains(body, `id="catalog-row-plain2"`) {
		t.Fatalf("expected the plain item's card too, got:\n%s", body)
	}
	if got := strings.Count(body, `img class="thumb"`); got != 1 {
		t.Fatalf("expected exactly 1 thumbnail <img> (the imaged item's own), got %d:\n%s", got, body)
	}
}

func TestCatalogPage_EmptyCatalogShowsPlaceholder(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	// No items at all -> the empty-state placeholder renders.

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="catalog-empty-row"`) {
		t.Fatalf("expected the empty-state placeholder, got:\n%s", body)
	}
}

// TestCatalogRowUpdateOOB_CarriesTheItemsRealThumbnail is the OOB-half of
// the same consistency requirement: an in-place card-update fragment must
// show the same real thumbnail the initial render would, not silently
// drop it.
func TestCatalogRowUpdateOOB_CarriesTheItemsRealThumbnail(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Existing", BasePrice: 100, IsActive: true})
	testsupport.SeedImage(t, db, "img-1", "itm1", "/public/assets/items/itm1/thumb.png")

	rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=Renamed&price=250&sku=S1&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("update: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("expected an in-place card update fragment:\n%s", body)
	}
	if !strings.Contains(body, "/public/assets/items/itm1/thumb.png") {
		t.Fatalf("card-update OOB fragment dropped the item's real thumbnail:\n%s", body)
	}
}
