package catalog

import (
	"context"
	"html"
	"net/http"
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

func TestModifierGroupAndOptionSetForms_NameControlCharsRefused(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/modifier-group", "itemId=itm1&name=Ext%1Bras&isActive=1")
	wantNameCharsBody(t, rec.Code, rec.Body.String())

	setID, err := data.NewOptionSetRepo(db).CreateOptionSet(context.Background(), "Sizes")
	if err != nil {
		t.Fatal(err)
	}
	rec = postForm(t, mux, "/api/catalog/option-set/rename", "optionSetId="+setID+"&name=Si%E2%80%AEzes")
	wantNameCharsBody(t, rec.Code, rec.Body.String())
	rec = postForm(t, mux, "/api/catalog/option-set", "name=Si%1Bzes")
	wantNameCharsBody(t, rec.Code, rec.Body.String())
}
