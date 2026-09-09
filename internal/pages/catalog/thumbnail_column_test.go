package catalog

// ut-docs#1842: the catalog list's thumbnail column must render an
// item's REAL thumbnail (item_images, role=thumbnail) instead of
// guessing a <id>/thumb.png file path, and must occupy no width at all
// when nothing in the listing has an image.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func TestCatalogPage_ThumbnailColumnHiddenWhenNoItemHasAnImage(t *testing.T) {
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
	if strings.Contains(body, "catalog-thumb-cell") {
		t.Fatalf("expected no thumbnail column when no item has an image, got:\n%s", body)
	}
}

func TestCatalogPage_ThumbnailColumnShowsRealImageAndPlaceholder(t *testing.T) {
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
	if !strings.Contains(body, `class="catalog-thumb-cell"`) {
		t.Fatalf("expected the thumbnail column once any item has an image, got:\n%s", body)
	}
	if !strings.Contains(body, "/public/assets/items/imaged1/thumb.png") {
		t.Fatalf("expected the imaged item's real thumbnail path rendered, got:\n%s", body)
	}
	// The imageless item in the same listing still gets its placeholder
	// box (column exists, this row just has nothing to show).
	if !strings.Contains(body, `<div class="thumb small" aria-hidden="true"></div>`) {
		t.Fatalf("expected the imageless item's placeholder box, got:\n%s", body)
	}
}

func TestCatalogPage_EmptyRowColspanMatchesColumnCount(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	// No items at all -> the empty-state row renders, with no thumbnail
	// column possible (nothing to have an image).

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `colspan="7"`) {
		t.Fatalf("expected empty-row colspan=7 with no thumbnail column, got:\n%s", body)
	}
	if strings.Contains(body, `colspan="8"`) {
		t.Fatalf("expected no colspan=8 (thumbnail column) when the catalog is empty, got:\n%s", body)
	}
}

// TestCatalogRowUpdateOOB_CarriesTheItemsRealThumbnail is the OOB-half of
// ut-docs#1842's consistency AC: an in-place row-update fragment must show
// the same real thumbnail the initial render would, not silently drop it.
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
		t.Fatalf("expected an in-place row update fragment:\n%s", body)
	}
	if !strings.Contains(body, "/public/assets/items/itm1/thumb.png") {
		t.Fatalf("row-update OOB fragment dropped the item's real thumbnail:\n%s", body)
	}
}

// TestItemDeactivate_LastImagedItemCollapsesWholeTable is ut-docs#1842
// review F2's exact scenario: deactivating the only active item that has
// a thumbnail must collapse the column everywhere, not just in the
// deleted row's own (now-removed) fragment — a plain row-delete fragment
// can't touch the <thead> or the remaining sibling rows, so this must be
// a whole-table swap too, mirroring the upload-side fix.
func TestItemDeactivate_LastImagedItemCollapsesWholeTable(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "imaged1", SKU: "I1", Name: "Imaged Item", BasePrice: 100, IsActive: true})
	testsupport.SeedImage(t, db, "img-1", "imaged1", "/public/assets/items/imaged1/thumb.png")
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "plain1", SKU: "P1", Name: "Plain Item", BasePrice: 100, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item/deactivate", "id=imaged1")
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	if !strings.Contains(got, `id="catalog-table" hx-swap-oob="true"`) {
		t.Fatalf("expected a whole-table OOB swap when the last imaged item is deactivated:\n%s", got)
	}
	if strings.Contains(got, "catalog-thumb-cell") {
		t.Fatalf("expected the thumbnail column gone entirely, got:\n%s", got)
	}
	if strings.Contains(got, `id="catalog-row-imaged1"`) {
		t.Fatalf("expected the deactivated item's row gone from the swapped table:\n%s", got)
	}
	if !strings.Contains(got, `id="catalog-row-plain1"`) {
		t.Fatalf("expected the surviving item's row still present:\n%s", got)
	}
}
