package catalog

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#3340: the item editor's "Age restricted" checkbox (ageRestricted)
// travels through parseItemInput -> CreateItem/UpdateItem exactly like
// isWeighed/stockUntracked: checked stores items.age_restricted = 1, an
// unchecked box (absent from the form) stores 0, and the catalog card
// carries data-age-restricted so openCatalogRow can re-check the box.

func ageRestrictedFlag(t *testing.T, db *sql.DB, id string) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`SELECT age_restricted FROM items WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("read age_restricted for %s: %v", id, err)
	}
	return v
}

func TestItemCreate_AgeRestrictedCheckboxPersists(t *testing.T) {
	mux, db := newCatalogMux(t)

	rec := postForm(t, mux, "/api/catalog/item", "name=Cider&price=350&sku=CIDER&isActive=1&ageRestricted=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("create: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM items WHERE sku = 'CIDER'`).Scan(&id); err != nil {
		t.Fatalf("find created item: %v", err)
	}
	if got := ageRestrictedFlag(t, db, id); got != 1 {
		t.Fatalf("checked ageRestricted: want age_restricted=1, got %d", got)
	}
	if !strings.Contains(rec.Body.String(), `data-age-restricted="1"`) {
		t.Fatalf("created row must carry data-age-restricted=\"1\" for the editor to re-check the box:\n%s", rec.Body.String())
	}

	rec = postForm(t, mux, "/api/catalog/item", "name=Bread&price=150&sku=BREAD&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("create bread: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := db.QueryRow(`SELECT id FROM items WHERE sku = 'BREAD'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if got := ageRestrictedFlag(t, db, id); got != 0 {
		t.Fatalf("absent ageRestricted: want age_restricted=0 (the safe default), got %d", got)
	}
}

func TestItemUpdate_AgeRestrictedCheckboxSetsAndClears(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Lager", BasePrice: 400, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=Lager&price=400&sku=S1&isActive=1&ageRestricted=on")
	if rec.Code != http.StatusOK {
		t.Fatalf("update (check): want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := ageRestrictedFlag(t, db, "itm1"); got != 1 {
		t.Fatalf("after checking: want 1, got %d", got)
	}
	if !strings.Contains(rec.Body.String(), `data-age-restricted="1"`) {
		t.Fatalf("updated row must carry data-age-restricted=\"1\":\n%s", rec.Body.String())
	}

	rec = postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=Lager&price=400&sku=S1&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("update (uncheck): want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := ageRestrictedFlag(t, db, "itm1"); got != 0 {
		t.Fatalf("after unchecking: want 0, got %d", got)
	}
}

func TestCatalogPage_RendersAgeRestrictedCheckbox(t *testing.T) {
	mux, _ := newCatalogMux(t)
	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog: want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="ageRestricted" id="item-age-restricted"`) {
		t.Fatalf("item editor must render the ageRestricted checkbox next to isWeighed/stockUntracked")
	}
	if !strings.Contains(body, "Age restricted") {
		t.Fatalf("checkbox label must be the translated catalog.age_restricted text")
	}
}
