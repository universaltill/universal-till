package catalog

// ut-docs#2119 — /catalog gains a category-filter chip row (BA/Architect/UX
// design recorded as comments on the issue). These tests pin that the
// handler actually loads and passes the data the template needs, following
// categories_page_test.go's "assert the render actually carries what it
// should" style (docs/reference/list-and-dialog-pattern.md).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func TestCatalogPage_RendersCategoryFilterChipRow(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedCategoryTree(t, db, "cat-food", "Food", "", 0, "")
	testsupport.SeedCategoryTree(t, db, "cat-drinks", "Drinks", "", 1, "")
	// A child category must NOT get its own chip (only top-level categories
	// do — a nested child folds into its parent's chip per the BA's
	// "selecting a parent includes its children" decision).
	testsupport.SeedCategoryTree(t, db, "cat-hot-drinks", "Hot Drinks", "cat-drinks", 0, "")
	// An inactive top-level category must not offer a chip either — a shop
	// owner can't browse into a category that no longer exists to browse
	// (same reasoning as ListActiveCategories itself).
	testsupport.SeedCategory(t, db, "cat-retired", "Retired", false)

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, `id="catalog-category-filter"`) {
		t.Fatalf("expected the category-filter chip row, id=catalog-category-filter; got:\n%s", body)
	}
	if !strings.Contains(body, `data-cat-all`) {
		t.Fatalf("expected the All-categories chip (data-cat-all); got:\n%s", body)
	}
	if !strings.Contains(body, `data-cat-id="cat-food"`) || !strings.Contains(body, `data-cat-id="cat-drinks"`) {
		t.Fatalf("expected chips for both top-level categories Food/Drinks; got:\n%s", body)
	}
	if strings.Contains(body, `data-cat-id="cat-hot-drinks"`) {
		t.Fatalf("expected NO separate chip for the nested child category; got:\n%s", body)
	}
	if strings.Contains(body, `data-cat-id="cat-retired"`) {
		t.Fatalf("expected NO chip for a deactivated category; got:\n%s", body)
	}
	// category-filter.js needs the FULL flat list (children included) to
	// walk parent-includes-children — this must ship even though only
	// top-level categories got their own chip above.
	if !strings.Contains(body, `"id":"cat-hot-drinks"`) || !strings.Contains(body, `"parentId":"cat-drinks"`) {
		t.Fatalf("expected CategoryNodesJSON to include the nested child with its parentId; got:\n%s", body)
	}
}

func TestCatalogPage_NoCategoriesStillRendersAllChipOnly(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-cat-all`) {
		t.Fatalf("expected the All-categories chip even with zero categories; got:\n%s", body)
	}
}
