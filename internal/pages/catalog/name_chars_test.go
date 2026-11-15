package catalog

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#5858: admin catalogue forms answer a name with a control or
// invisible character with the translated 400, never err.Error() or the
// generic "invalid request" line.
func wantNameCharsBody(t *testing.T, code int, body string) {
	t.Helper()
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", code, body)
	}
	want := httpx.T("en", "common.error.name_invalid_characters")
	if want == "common.error.name_invalid_characters" || !strings.Contains(html.UnescapeString(body), want) {
		t.Fatalf("body = %q, want the translated name text %q", body, want)
	}
	if strings.Contains(body, data.ErrNameInvalidCharacters.Error()) || strings.Contains(strings.ToLower(body), "invalid request") {
		t.Fatalf("body leaks the raw or generic error: %q", body)
	}
}

func TestItemAndVariantForms_NameControlCharsRefused(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item", "name=Co%1Bla&price=200&sku=NEW&isActive=1")
	wantNameCharsBody(t, rec.Code, rec.Body.String())
	rec = postForm(t, mux, "/api/catalog/item", "name=Co%E2%80%AEla&price=200&sku=NEW&isActive=1")
	wantNameCharsBody(t, rec.Code, rec.Body.String())
	rec = postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=Co%1Bla&price=300&sku=S1&isActive=1")
	wantNameCharsBody(t, rec.Code, rec.Body.String())
	rec = postForm(t, mux, "/api/catalog/variant", "itemId=itm1&name=5%1B0ml&sku=S1-L&price=350&isActive=1")
	wantNameCharsBody(t, rec.Code, rec.Body.String())

	var name string
	if err := db.QueryRow(`SELECT name FROM items WHERE id = 'itm1'`).Scan(&name); err != nil || name != "Coffee" {
		t.Fatalf("stored name = %q, %v", name, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("item rows = %d, %v", n, err)
	}
}

// postHtmx posts the way the real screen's hx-post forms do.
func postHtmx(t *testing.T, mux *http.ServeMux, path, form, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", target)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The modifiers and option-sets screens swap only text/html (app.js
// htmx:beforeSwap); a text/plain 400 would show the generic banner, so the
// refusal must come back as the screen's own fragment holding the message.
func wantFragmentNameChars(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html (body %q)", ct, rec.Body.String())
	}
	wantNameCharsBody(t, rec.Code, rec.Body.String())
}

func TestModifierGroupAndOptionSetForms_NameControlCharsRefused(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	ctx := context.Background()

	// Modifiers screen: group and option create/update.
	wantFragmentNameChars(t, postHtmx(t, mux, "/api/catalog/modifier-group", "name=Ext%1Bras&isActive=1", "modifiers-list"))
	modRepo := data.NewModifierRepo(db)
	if _, err := modRepo.CreateGroup(ctx, "g1", "Extras", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := modRepo.CreateOption(ctx, "o1", "g1", "Milk", 0, 0); err != nil {
		t.Fatal(err)
	}
	wantFragmentNameChars(t, postHtmx(t, mux, "/api/catalog/modifier-group", "id=g1&name=Ext%E2%80%AEras&isActive=1", "modifiers-list"))
	wantFragmentNameChars(t, postHtmx(t, mux, "/api/catalog/modifier-option", "groupId=g1&name=Oa%E2%80%AEt", "modifiers-list"))
	wantFragmentNameChars(t, postHtmx(t, mux, "/api/catalog/modifier-option", "groupId=g1&id=o1&name=Mi%1Blk&isActive=1", "modifiers-list"))

	// Option-sets screen: create, add value, rename.
	repo := data.NewOptionSetRepo(db)
	setID, err := repo.CreateOptionSet(ctx, "Sizes")
	if err != nil {
		t.Fatal(err)
	}
	wantFragmentNameChars(t, postHtmx(t, mux, "/api/catalog/option-set/rename", "optionSetId="+setID+"&name=Si%E2%80%AEzes", "option-sets-list"))
	wantFragmentNameChars(t, postHtmx(t, mux, "/api/catalog/option-set", "name=Si%1Bzes", "option-sets-list"))
	wantFragmentNameChars(t, postHtmx(t, mux, "/api/catalog/option-set-value", "optionSetId="+setID+"&value=Sm%E2%80%AEall", "option-sets-list"))
}
