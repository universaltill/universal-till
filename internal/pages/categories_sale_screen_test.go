package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// Manage-shop catalog contract §7.2(5): the till's own category editor can
// set what the cloud's save_category can — "Show on the sale screen" — and
// the list marks a hidden category. The checkbox rides with a presence
// marker, so an older page (or any post without the marker) never reads an
// absent box as "hide".
func TestCategoryDialog_ShowOnSaleScreenToggleAndBadge(t *testing.T) {
	mux, d := newCategoriesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	hidden := func(id string) int {
		t.Helper()
		var h int
		if err := d.Db.QueryRow(`SELECT sell_screen_hidden FROM categories WHERE id = ?`, id).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}

	// Create with the box unticked → hidden.
	rec := postCategoryMultipart(t, mux, "/api/categories", []catPart{{"name", "Specials"}, {"show_on_sale_screen_field", "1"}}, nil, manager)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := d.Db.QueryRow(`SELECT id FROM categories WHERE name = 'Specials'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if hidden(id) != 1 {
		t.Fatal("an unticked box must hide the category")
	}

	// The list shows the badge and prefills the box unticked.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/categories", nil), manager)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	body := rr.Body.String()
	for _, want := range []string{`data-field-show_on_sale_screen="0"`, `class="chip category-hidden-badge"`, `name="show_on_sale_screen" value="1" checked`, `name="show_on_sale_screen_field" value="1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /categories missing %s", want)
		}
	}

	// A post without the marker leaves the flag alone.
	if rec := postCategoryMultipart(t, mux, "/api/categories/"+id, []catPart{{"name", "Specials"}}, nil, manager); rec.Code != http.StatusOK || hidden(id) != 1 {
		t.Fatalf("no marker: code=%d hidden=%d", rec.Code, hidden(id))
	}
	// Ticked → shown again.
	if rec := postCategoryMultipart(t, mux, "/api/categories/"+id, []catPart{{"name", "Specials"}, {"show_on_sale_screen_field", "1"}, {"show_on_sale_screen", "1"}}, nil, manager); rec.Code != http.StatusOK || hidden(id) != 0 {
		t.Fatalf("tick: code=%d hidden=%d", rec.Code, hidden(id))
	}
}
