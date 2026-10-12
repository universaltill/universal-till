package catalog

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#4214: the item editor's Type select (itemType) travels through
// parseItemInput -> CreateItem/UpdateItem. A gift voucher may leave its
// price empty (stored as 0 — the popup asks for the value at sale) and is
// never stock-tracked; an unknown type is refused.

func itemTypeRow(t *testing.T, db *sql.DB, sku string) (typ string, price int64, untracked int) {
	t.Helper()
	if err := db.QueryRow(`SELECT item_type, base_price, stock_untracked FROM items WHERE sku = ?`, sku).Scan(&typ, &price, &untracked); err != nil {
		t.Fatalf("read item %s: %v", sku, err)
	}
	return
}

func TestItemCreate_GiftVoucherType(t *testing.T) {
	mux, db := newCatalogMux(t)
	rec := postForm(t, mux, "/api/catalog/item", "name=Gift+voucher&price=&sku=GV1&isActive=1&itemType=gift_voucher")
	if rec.Code != http.StatusOK {
		t.Fatalf("create gift voucher with empty price: %d %s", rec.Code, rec.Body.String())
	}
	typ, price, untracked := itemTypeRow(t, db, "GV1")
	if typ != "gift_voucher" || price != 0 || untracked != 1 {
		t.Fatalf("got type=%q price=%d untracked=%d, want gift_voucher/0/1", typ, price, untracked)
	}
	if !strings.Contains(rec.Body.String(), `data-item-type="gift_voucher"`) {
		t.Fatalf("the catalog row must carry data-item-type:\n%s", rec.Body.String())
	}
	// A standard item still needs its price, and keeps type standard.
	if rec := postForm(t, mux, "/api/catalog/item", "name=Bread&price=&sku=BR1&isActive=1"); rec.Code == http.StatusOK {
		t.Fatalf("a standard item with no price must be refused")
	}
	postForm(t, mux, "/api/catalog/item", "name=Bread&price=200&sku=BR2&isActive=1")
	if typ, _, _ := itemTypeRow(t, db, "BR2"); typ != "standard" {
		t.Fatalf("no itemType = standard, got %q", typ)
	}
	if rec := postForm(t, mux, "/api/catalog/item", "name=X&price=100&sku=X1&isActive=1&itemType=deposit"); rec.Code == http.StatusOK {
		t.Fatalf("an unknown item type must be refused")
	}
}

func TestItemUpdate_ItemTypeSetsAndClears(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Voucher", BasePrice: 2500, IsActive: true})
	if rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=Voucher&price=2500&sku=S1&isActive=1&itemType=gift_voucher"); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if typ, price, _ := itemTypeRow(t, db, "S1"); typ != "gift_voucher" || price != 2500 {
		t.Fatalf("after set: %q %d", typ, price)
	}
	if rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=Voucher&price=2500&sku=S1&isActive=1&itemType=standard"); rec.Code != http.StatusOK {
		t.Fatalf("update back: %d %s", rec.Code, rec.Body.String())
	}
	if typ, _, _ := itemTypeRow(t, db, "S1"); typ != "standard" {
		t.Fatalf("after clear: %q", typ)
	}
}

func TestCatalogPage_RendersItemTypeSelect(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	body := rec.Body.String()
	for _, want := range []string{`name="itemType" id="item-type"`, `<option value="gift_voucher">Gift voucher</option>`, `<option value="standard">Standard item</option>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("item editor missing %q", want)
		}
	}
}
