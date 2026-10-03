package catalogsync

import (
	"net/url"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		method, path string
		form         url.Values
		wantOK       bool
		wantPattern  string
		wantID       string
	}{
		{"POST", "/api/catalog/item/update", url.Values{"id": {"itm1"}}, true, "POST /api/catalog/item/update", "itm1"},
		{"POST", "/api/catalog/item", url.Values{"name": {"x"}}, true, "POST /api/catalog/item", ""},
		// A literal route wins over a wildcard one, as on ServeMux.
		{"POST", "/api/categories/reorder", nil, true, "POST /api/categories/reorder", ""},
		{"POST", "/api/categories/cat1", nil, true, "POST /api/categories/{id}", "cat1"},
		{"POST", "/api/categories/cat1/active", nil, true, "POST /api/categories/{id}/active", "cat1"},
		{"POST", "/api/designer/categories/cat9", nil, true, "POST /api/designer/categories/{id}", "cat9"},
		// First non-empty of the listed form fields.
		{"POST", "/api/catalog/barcode", url.Values{"panelItem": {"itm2"}}, true, "POST /api/catalog/barcode", "itm2"},
		{"POST", "/api/catalog/barcode", url.Values{"itemId": {"itm1"}, "panelItem": {"itm2"}}, true, "POST /api/catalog/barcode", "itm1"},
		// ut-docs#3606: buttons/add is checked on the item's button row
		// (the one AddButton writes), whatever code is posted.
		{"POST", "/api/buttons/add", url.Values{"code": {"NEWCODE"}, "itemId": {"itm1"}}, true, "POST /api/buttons/add", "itm1"},
		{"POST", "/api/buttons/add", url.Values{"code": {"NEWCODE"}, "item_id": {"itm2"}}, true, "POST /api/buttons/add", "itm2"},
		// Not travelling: wrong method, unknown route, an empty wildcard,
		// photo upload routes, out-of-scope routes.
		{"GET", "/api/catalog/item/update", nil, false, "", ""},
		{"POST", "/api/categories/", nil, false, "", ""},
		{"POST", "/api/catalog/item/image", nil, false, "", ""},
		{"POST", "/api/catalog/option-set", nil, false, "", ""},
		{"POST", "/api/catalog/barcode-backfill", nil, false, "", ""},
		{"POST", "/api/sync/catalog/apply", nil, false, "", ""},
	}
	for _, c := range cases {
		rt, id, ok := Resolve(c.method, c.path, c.form)
		if ok != c.wantOK || rt.Pattern != c.wantPattern || id != c.wantID {
			t.Errorf("Resolve(%s %s) = %q %q %v, want %q %q %v", c.method, c.path, rt.Pattern, id, ok, c.wantPattern, c.wantID, c.wantOK)
		}
	}
}

func TestRoutes_KindsAreKnownToTheConflictCheck(t *testing.T) {
	known := map[string]bool{
		data.CatalogKindItem: true, data.CatalogKindVariant: true, data.CatalogKindCategory: true,
		data.CatalogKindButton: true, data.CatalogKindModifierGroup: true,
		data.CatalogKindItemButton: true,
	}
	seen := map[string]bool{}
	for _, rt := range Routes {
		if !known[rt.Kind] {
			t.Errorf("%s: unknown kind %q", rt.Pattern, rt.Kind)
		}
		if seen[rt.Pattern] {
			t.Errorf("%s listed twice", rt.Pattern)
		}
		seen[rt.Pattern] = true
		if rt.Conflict && rt.IDFrom == "" {
			t.Errorf("%s: a conflict-checked route must name its record", rt.Pattern)
		}
	}
}

func TestRoutes_ButtonAddIsCheckedOnTheItemsButtonRow(t *testing.T) {
	rt, _, ok := Resolve("POST", "/api/buttons/add", url.Values{"itemId": {"itm1"}})
	if !ok || rt.Kind != data.CatalogKindItemButton || !rt.Conflict {
		t.Fatalf("buttons/add route = %+v, want kind %q, conflict-checked", rt, data.CatalogKindItemButton)
	}
}
