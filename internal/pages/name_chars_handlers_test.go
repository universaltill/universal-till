package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#5858: a name with a control or invisible character is refused with
// the translated common.error.name_invalid_characters text, not a generic
// "could not create" line, and nothing is written.
func wantNameCharsMsg(t *testing.T, code int, body string) {
	t.Helper()
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", code, body)
	}
	want := httpx.T("en", "common.error.name_invalid_characters")
	if want == "common.error.name_invalid_characters" || !strings.Contains(body, strings.ReplaceAll(want, "'", "&#39;")) && !strings.Contains(body, want) {
		t.Fatalf("body = %q, want the translated name text %q", body, want)
	}
}

func TestCategoriesPage_NameControlCharsRefused(t *testing.T) {
	mux, d := newCategoriesTestMux(t)
	initPagesI18n(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postFormHtmx(mux, "/api/categories", url.Values{"name": {"Dr\x1binks"}}, &manager)
	wantNameCharsMsg(t, rec.Code, rec.Body.String())
	rec = postForm(mux, "/api/categories", url.Values{"name": {"Dr\u202einks"}}, &manager)
	if got := rec.Header().Get("Location"); got != "/categories?err=common.error.name_invalid_characters" {
		t.Fatalf("redirect = %q", got)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM categories WHERE name LIKE 'Dr%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows written = %d, %v", n, err)
	}
	postForm(mux, "/api/categories", url.Values{"name": {"Drinks"}}, &manager)
	var id string
	if err := d.Db.QueryRow(`SELECT id FROM categories WHERE name = 'Drinks'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rec = postFormHtmx(mux, "/api/categories/"+id, url.Values{"name": {"Dr\x1binks"}}, &manager)
	wantNameCharsMsg(t, rec.Code, rec.Body.String())
}

func TestDesignerCategoriesAPI_NameControlCharsRefused(t *testing.T) {
	mux, d := newDesignerCategoriesMux(t)
	seedDesignerCategories(t, d)
	mgr := auth.User{ID: "m1", Role: "manager"}
	rec := postForm(mux, "/api/designer/categories", url.Values{"name": {"Ba\x1bkery"}}, &mgr)
	wantNameCharsMsg(t, rec.Code, rec.Body.String())
	rec = postForm(mux, "/api/designer/categories/cat-a", url.Values{"name": {"Ba\u202ekery"}}, &mgr)
	wantNameCharsMsg(t, rec.Code, rec.Body.String())
}

func TestRegistersPage_NameControlCharsRefused(t *testing.T) {
	mux, d := newRegistersTestMux(t)
	initPagesI18n(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postFormHtmx(mux, "/api/registers", url.Values{"name": {"Front\x1bTill"}}, &manager)
	wantNameCharsMsg(t, rec.Code, rec.Body.String())
	postForm(mux, "/api/registers", url.Values{"name": {"Front Till"}}, &manager)
	var id string
	if err := d.Db.QueryRow(`SELECT id FROM registers WHERE name = 'Front Till'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rec = postFormHtmx(mux, "/api/registers/"+id, url.Values{"name": {"Front\u202eTill"}}, &manager)
	wantNameCharsMsg(t, rec.Code, rec.Body.String())
}

func TestTaxCodesAPI_NameControlCharsRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newTaxCodesTestDeps(t)
	for _, form := range []string{"name=VAT%1B&rate=10", "name=VAT%E2%80%AE&rate=10"} {
		rec := postFormString(mux, "/api/catalog/tax-codes", form)
		wantNameCharsMsg(t, rec.Code, rec.Body.String())
	}
	rec := postFormString(mux, "/api/catalog/tax-codes/update", "id=tax_std&name=Std%1B&rate=20&isActive=1")
	wantNameCharsMsg(t, rec.Code, rec.Body.String())
}

func postFormString(mux *http.ServeMux, path, form string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
