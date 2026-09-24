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

// Review finding 6: the "Show on the sale screen" flag is written in the
// same statement as the create/update, so a failure writing it leaves
// nothing half-saved (no new row, no renamed category). A trigger that
// refuses sell_screen_hidden = 1 forces that failure.
func TestCategoryDialog_HiddenFlagFailureLeavesNothingHalfSaved(t *testing.T) {
	mux, d := newCategoriesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	if _, err := d.Db.Exec(`INSERT INTO categories (id, name, sort_order, is_active) VALUES ('cat-keep', 'Keep', 0, 1)`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TRIGGER t_refuse_hidden_ins BEFORE INSERT ON categories WHEN NEW.sell_screen_hidden = 1 BEGIN SELECT RAISE(ABORT, 'forced'); END`,
		`CREATE TRIGGER t_refuse_hidden_upd BEFORE UPDATE ON categories WHEN NEW.sell_screen_hidden = 1 BEGIN SELECT RAISE(ABORT, 'forced'); END`,
	} {
		if _, err := d.Db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := d.Db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	postCategoryMultipart(t, mux, "/api/categories", []catPart{{"name", "Specials"}, {"show_on_sale_screen_field", "1"}}, nil, manager)
	if n := count(`SELECT COUNT(*) FROM categories WHERE name = 'Specials'`); n != 0 {
		t.Fatal("a create whose hidden flag failed left the category behind")
	}
	postCategoryMultipart(t, mux, "/api/categories/cat-keep", []catPart{{"name", "Renamed"}, {"show_on_sale_screen_field", "1"}}, nil, manager)
	if n := count(`SELECT COUNT(*) FROM categories WHERE id = 'cat-keep' AND name = 'Keep'`); n != 1 {
		t.Fatal("an update whose hidden flag failed still renamed the category")
	}
}
