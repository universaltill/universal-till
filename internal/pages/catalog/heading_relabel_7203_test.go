package catalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/uislot"
)

// ut-docs#7203: a shop type that relabels the Items rail's /catalog section
// (service: "Services") must see the same word as the panel heading.
func TestCatalogPage_HeadingFollowsShopTypeRelabel_7203(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()

	mux := http.NewServeMux()
	Register(mux, &common.Deps{
		Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{},
		ItemsAmendments: []uislot.Amendment{{Slot: uislot.ItemsSlot, Key: "/catalog", LabelKey: "layout.salon.services"}},
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<title>Services") {
		t.Errorf("browser title should follow the relabel too; body: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "<h1>Services</h1>") {
		t.Errorf("heading should be the relabelled \"Services\"; body: %s", rec.Body.String())
	}
}

func TestCatalogPage_HeadingDefaultsToCatalog_7203(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if !strings.Contains(rec.Body.String(), "<h1>Catalog</h1>") {
		t.Errorf("default heading should stay \"Catalog\"")
	}
}
