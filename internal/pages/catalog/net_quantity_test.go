package catalog

// ut-docs#3391: the item form's net-quantity pair (netQuantityValue +
// netQuantityUnit) persists through create/update, a blank value stores
// "none" whatever the unit select says, and an invalid unit or value is
// refused server-side with 400 — never trusted from the client.

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
)

func readNetQuantity(t *testing.T, db *sql.DB, name string) (string, sql.NullInt64, sql.NullString) {
	t.Helper()
	var id string
	var v sql.NullInt64
	var u sql.NullString
	if err := db.QueryRow(`SELECT id, net_quantity_value, net_quantity_unit FROM items WHERE name = ?`, name).Scan(&id, &v, &u); err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return id, v, u
}

func TestItemForm_NetQuantityPersists(t *testing.T) {
	mux, db := newCatalogMux(t)

	if rec := postForm(t, mux, "/api/catalog/item", "name=Rice&price=200&netQuantityValue=500&netQuantityUnit=g"); rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	id, v, u := readNetQuantity(t, db, "Rice")
	if !v.Valid || v.Int64 != 500 || !u.Valid || u.String != "g" {
		t.Fatalf("stored %+v %+v, want 500 g", v, u)
	}

	// The catalog card carries it for the edit dialog to prefill.
	if body := get(t, mux, "/catalog").Body.String(); !strings.Contains(body, `data-net-qty="500"`) || !strings.Contains(body, `data-net-qty-unit="g"`) {
		t.Fatalf("catalog card lacks data-net-qty/data-net-qty-unit for Rice")
	}

	// Update to a 6-pack counted per item.
	if rec := postForm(t, mux, "/api/catalog/item/update", "id="+id+"&name=Rice&price=200&netQuantityValue=6&netQuantityUnit=ea"); rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	_, v, u = readNetQuantity(t, db, "Rice")
	if v.Int64 != 6 || u.String != "ea" {
		t.Fatalf("stored %+v %+v, want 6 ea", v, u)
	}

	// A blank value clears it, even though the <select> always submits a
	// unit (a weighed item's disabled fields submit nothing at all —
	// same result).
	if rec := postForm(t, mux, "/api/catalog/item/update", "id="+id+"&name=Rice&price=200&netQuantityValue=&netQuantityUnit=g"); rec.Code != http.StatusOK {
		t.Fatalf("clear = %d: %s", rec.Code, rec.Body.String())
	}
	_, v, u = readNetQuantity(t, db, "Rice")
	if v.Valid || u.Valid {
		t.Fatalf("stored %+v %+v, want NULL NULL", v, u)
	}
}

func TestItemForm_NetQuantityRejectsInvalid(t *testing.T) {
	mux, db := newCatalogMux(t)
	for _, form := range []string{
		"name=A&price=100&netQuantityValue=500&netQuantityUnit=kg",
		"name=B&price=100&netQuantityValue=500&netQuantityUnit=",
		"name=C&price=100&netQuantityValue=0&netQuantityUnit=g",
		"name=D&price=100&netQuantityValue=-3&netQuantityUnit=g",
		"name=E&price=100&netQuantityValue=1.5&netQuantityUnit=g",
	} {
		rec := postForm(t, mux, "/api/catalog/item", form)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid net quantity") {
			t.Errorf("%s = %d %q, want 400 invalid net quantity", form, rec.Code, rec.Body.String())
		}
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n)
	if n != 0 {
		t.Fatalf("rejected input created %d items", n)
	}
}
