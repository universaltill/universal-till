package pages

// ut-docs#2119 — /inventory gains the same category-filter chip row as
// /catalog (BA/Architect/UX design recorded as comments on the issue).
// Follows inventory_prediction_test.go's setup (real migrations via
// db.Open, registerInventoryPage directly) and categories_page_test.go's
// "assert the render actually carries what it should" style.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

func TestInventoryPage_RendersCategoryFilterChipRowAndRowCategoryID(t *testing.T) {
	chdirRoot(t)
	f := filepath.Join(t.TempDir(), "inv-cat-2119.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v (%s)", err, q)
		}
	}
	mustExec(`INSERT INTO categories (id, name) VALUES ('cat-drinks', 'Drinks')`)
	mustExec(`INSERT INTO items (id, name, sku, base_price, category_id, is_active) VALUES
		('it-cola', 'Cola', 'COLA', 100, 'cat-drinks', 1)`)
	mustExec(`INSERT INTO stock_locations (id, name) VALUES ('loc-1', 'Shop floor')`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-1', 'it-cola', 'loc-1', 6)`)

	state := common.LoadState(context.Background(), settings.NewStore(d), &config.Config{Theme: "default"})
	dp := &common.Deps{Cfg: &config.Config{Theme: "default"}, Db: d, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(d)}
	mux := http.NewServeMux()
	registerInventoryPage(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/inventory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, `id="inventory-category-filter"`) {
		t.Fatalf("expected the category-filter chip row, id=inventory-category-filter; got:\n%s", body)
	}
	if !strings.Contains(body, `data-cat-all`) {
		t.Fatalf("expected the All-categories chip; got:\n%s", body)
	}
	if !strings.Contains(body, `data-cat-id="cat-drinks"`) {
		t.Fatalf("expected a chip for the top-level Drinks category; got:\n%s", body)
	}
	// The stock row itself must carry the item's category id, mirroring
	// catalog_row.html's own data-category (ut-docs#1951).
	if !strings.Contains(body, `data-category="cat-drinks"`) {
		t.Fatalf("expected the stock row to carry data-category=\"cat-drinks\"; got:\n%s", body)
	}
}
