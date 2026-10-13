package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#4214: a gift voucher item needs staff to take its value and card
// number, so the self-order kiosk (and table-QR ordering, the same
// surface) never lists it, and a crafted scan of its code adds nothing.
func TestSelfOrderShop_GiftVoucherItemNotListedOrSellable(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedShopItem(t, d, "itm-gv", "GV", "5000003", "Gift voucher", 2500)
	seedStock(t, d, "itm-coffee", 10)
	if _, err := d.DB.Exec(`UPDATE items SET item_type = 'gift_voucher' WHERE id = 'itm-gv'`); err != nil {
		t.Fatal(err)
	}
	items, err := loadShopItems(context.Background(), dp)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ItemID == "itm-gv" {
			t.Fatalf("a gift voucher item must not be listed on the kiosk: %+v", it)
		}
	}
	if len(items) != 1 {
		t.Fatalf("the normal item stays listed, got %d items", len(items))
	}
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)
	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000003"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if n := len(dp.KioskEngine.Basket().Lines); n != 0 {
		t.Fatalf("a kiosk scan of a gift voucher item must add nothing, got %d lines", n)
	}
}
