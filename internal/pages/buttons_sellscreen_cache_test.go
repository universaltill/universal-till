package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ut-docs#2501: the sell-screen tile cache end to end through the real mux —
// registerButtonsAPI's shared cache, real migrated DB (openPagesTestDB),
// and a change made through ANOTHER page's real route (the catalog's
// "Delete item"), which knows nothing about the cache: the database's own
// change counters are the only thing that can invalidate it.
func TestButtonsUIFragment_CacheInvalidatedByCatalogDeactivate(t *testing.T) {
	mux, _ := newButtonsAndCatalogMux(t)
	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d (%s)", path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	first := get("/ui/buttons")
	if !strings.Contains(first, "Apple") {
		t.Fatalf("seeded Apple tile missing from /ui/buttons:\n%s", first)
	}
	// Second render — the cached one once the first-render setting seed has
	// settled — must still show it.
	get("/ui/buttons")
	if again := get("/ui/buttons"); !strings.Contains(again, "Apple") {
		t.Fatal("cached /ui/buttons lost the Apple tile")
	}
	if pop := get("/ui/buttons/category?id="); !strings.Contains(pop, "Apple") {
		t.Fatal("uncategorized popup has no Apple tile")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/deactivate", strings.NewReader(url.Values{"id": {"itm1"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate = %d (%s)", rec.Code, rec.Body.String())
	}

	if after := get("/ui/buttons"); strings.Contains(after, "Apple") {
		t.Fatal("/ui/buttons still offers the deactivated Apple (stale sell-screen cache)")
	}
	if pop := get("/ui/buttons/category?id="); strings.Contains(pop, "Apple") {
		t.Fatal("category popup still offers the deactivated Apple (stale sell-screen cache)")
	}
}

// The request locale is part of the key: a German request right after an
// English one must not be served the English bytes.
func TestButtonsUIFragment_CacheKeyedOnLocale(t *testing.T) {
	mux, _ := newButtonsMux(t)
	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	get("/ui/buttons?lang=en")
	en := get("/ui/buttons?lang=en")
	de := get("/ui/buttons?lang=de")
	if en == de {
		t.Fatal("German /ui/buttons was served the cached English fragment")
	}
	if !strings.Contains(de, "Apple") {
		t.Fatal("German render has no Apple tile")
	}
}
