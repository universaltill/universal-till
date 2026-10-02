package catalog

// ut-docs#3097: "Generate missing SKUs" on the Catalog page — preview
// (GET, no writes) lists each SKU-less item with the SKU it would get;
// commit (POST, primary only) assigns them; the action-row button shows
// only on a primary till with at least one SKU-less item, carrying the
// count.

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func seedSKULessItems(t *testing.T, db *sql.DB) {
	t.Helper()
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "has-sku", SKU: "COF-1", Name: "Coffee", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('no-sku-1', NULL, 'Legacy Scone', 100, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('no-sku-2', '', 'Legacy Muffin', 100, 1)`); err != nil {
		t.Fatal(err)
	}
}

func missingSKUCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	n, err := data.NewCatalogRepo(db).CountItemsMissingSKU(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func newReplicaCatalogMux(t *testing.T) (*http.ServeMux, *sql.DB) {
	t.Helper()
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
	st := settings.NewStore(db)
	if err := st.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}, Settings: st})
	return mux, db
}

func TestSKUBackfillPreview_ListsItemsAndProposedSKUs(t *testing.T) {
	mux, db := newCatalogMux(t)
	seedSKULessItems(t, db)

	rec := get(t, mux, "/api/catalog/sku-backfill")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Legacy Scone", "Legacy Muffin", "ITEM-0001", "ITEM-0002", "2 item(s) have no SKU"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Coffee") {
		t.Errorf("preview lists an item that already has a SKU: %s", body)
	}
	if !strings.Contains(body, `hx-post="/api/catalog/sku-backfill"`) {
		t.Errorf("preview must offer the confirm button: %s", body)
	}
	if n := missingSKUCount(t, db); n != 2 {
		t.Fatalf("preview wrote SKUs: %d still missing, want 2", n)
	}
}

func TestSKUBackfillCommit_AssignsThenPreviewEmpty(t *testing.T) {
	mux, db := newCatalogMux(t)
	seedSKULessItems(t, db)

	rec := postForm(t, mux, "/api/catalog/sku-backfill", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("commit code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Assigned 2 SKU(s).") {
		t.Fatalf("result must report the count: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "close:sku-backfill-modal refresh-region") {
		t.Fatalf("result Close must refresh the catalog grid: %s", rec.Body.String())
	}
	if n := missingSKUCount(t, db); n != 0 {
		t.Fatalf("%d items still miss a SKU after commit", n)
	}

	again := get(t, mux, "/api/catalog/sku-backfill").Body.String()
	if !strings.Contains(again, "Every item already has a SKU") {
		t.Fatalf("preview after commit must show the empty state: %s", again)
	}
	if strings.Contains(again, `hx-post="/api/catalog/sku-backfill"`) {
		t.Fatalf("empty preview must not offer a confirm button: %s", again)
	}
}

func TestSKUBackfillCommit_RefusedOnReplica(t *testing.T) {
	mux, db := newReplicaCatalogMux(t)
	seedSKULessItems(t, db)

	rec := postForm(t, mux, "/api/catalog/sku-backfill", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("replica commit = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "manage the catalog on the primary till") {
		t.Fatalf("replica refusal must carry item_replica_use_primary's message: %s", rec.Body.String())
	}
	if n := missingSKUCount(t, db); n != 2 {
		t.Fatalf("replica commit wrote SKUs: %d still missing, want 2", n)
	}
}

func skuCatalogPage(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/catalog = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestCatalogPage_SKUBackfillButtonShowsCountOnPrimary(t *testing.T) {
	mux, db := newCatalogMux(t)
	seedSKULessItems(t, db)

	body := skuCatalogPage(t, mux)
	if !strings.Contains(body, `id="catalog-sku-backfill-btn"`) {
		t.Fatalf("primary with SKU-less items must show the button: %s", body)
	}
	if !strings.Contains(body, "Generate missing SKUs (2)") {
		t.Fatalf("button must carry the count: %s", body)
	}
	if !strings.Contains(body, `id="sku-backfill-modal"`) {
		t.Fatalf("page must carry the dialog the button opens")
	}
}

func TestCatalogPage_SKUBackfillButtonHiddenWhenNoneMissing(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "has-sku", SKU: "COF-1", Name: "Coffee", BasePrice: 100, IsActive: true})

	if body := skuCatalogPage(t, mux); strings.Contains(body, `id="catalog-sku-backfill-btn"`) {
		t.Fatalf("button must be hidden when every item has a SKU")
	}
}

func TestCatalogPage_SKUBackfillButtonHiddenOnReplica(t *testing.T) {
	mux, db := newReplicaCatalogMux(t)
	seedSKULessItems(t, db)

	if body := skuCatalogPage(t, mux); strings.Contains(body, `id="catalog-sku-backfill-btn"`) {
		t.Fatalf("button must be hidden on a replica till")
	}
}
