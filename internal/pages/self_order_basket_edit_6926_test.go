package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const kioskBasketLockedMsg = "Your order is being placed. Wait for it to finish, then add more."

// ut-docs#6926: while a kiosk basket's checkout holds its tender claim, every
// kiosk basket mutation on THAT engine is refused (409, cart re-rendered with
// the toast, basket unchanged); after the claim is released edits work again.
func TestSelfOrder_BasketEditsRefusedWhileCheckoutRuns_6926(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedShopItem(t, d, "itm-tea", "TEA", "5000002", "Tea", 250)
	seedStock(t, d, "itm-coffee", 10)
	seedStock(t, d, "itm-tea", 10)

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)
	eng := dp.KioskEngine

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := post("/api/self-order/scan", "code=5000001"); rec.Code != http.StatusOK {
		t.Fatalf("precondition scan: %d %s", rec.Code, rec.Body.String())
	}
	lines := eng.Lines()
	if len(lines) != 1 {
		t.Fatalf("precondition: %d lines", len(lines))
	}
	key := lines[0].LineKey
	qty := lines[0].Qty
	lineOrderType := lines[0].OrderType
	basketOrderType := eng.Basket().OrderType

	if !eng.TryBeginTender() {
		t.Fatal("TryBeginTender refused on idle engine")
	}
	released := false
	defer func() {
		if !released {
			eng.EndTender()
		}
	}()

	cases := []struct{ name, path, body string }{
		{"scan", "/api/self-order/scan", "code=5000002"},
		{"scan-with-modifiers", "/api/self-order/scan-with-modifiers", "code=5000002&itemId=itm-tea"},
		{"line", "/api/self-order/line", "key=" + key + "&delta=1"},
		{"order-type", "/api/self-order/order-type", "order_type=takeaway"},
		{"remove", "/api/self-order/remove", "key=" + key},
	}
	for _, c := range cases {
		rec := post(c.path, c.body)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s: want 409, got %d: %s", c.name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), kioskBasketLockedMsg) {
			t.Errorf("%s: response lacks the locked message: %s", c.name, rec.Body.String())
		}
		got := eng.Lines()
		if len(got) != 1 || got[0].LineKey != key || got[0].Qty != qty || got[0].OrderType != lineOrderType {
			t.Errorf("%s: basket changed while checkout runs: %+v", c.name, got)
		}
		if ot := eng.Basket().OrderType; ot != basketOrderType {
			t.Errorf("%s: order type changed while checkout runs: %q -> %q", c.name, basketOrderType, ot)
		}
	}

	eng.EndTender()
	released = true
	if rec := post("/api/self-order/scan", "code=5000002"); rec.Code != http.StatusOK {
		t.Fatalf("scan after EndTender: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(eng.Lines()); n != 2 {
		t.Fatalf("after EndTender want 2 lines, got %d", n)
	}
}

// AC2: one table session's running checkout does not block another's basket.
func TestSelfOrder_BasketEditRefusalIsPerBasket_6926(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	tableA := createSelfOrderTable(t, dp, "T1", 100)
	tableB := createSelfOrderTable(t, dp, "T2", 200)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	registerSelfOrderShop(mux, dp)

	a := newSelfOrderGuest(t, mux)
	a.get("/self-order?table=" + tableA)
	b := newSelfOrderGuest(t, mux)
	b.get("/self-order?table=" + tableB)

	if !a.engine(dp).TryBeginTender() {
		t.Fatal("TryBeginTender refused")
	}
	defer a.engine(dp).EndTender()

	if rec := a.post("/api/self-order/scan", "code=5000001"); rec.Code != http.StatusConflict {
		t.Fatalf("A while paying: want 409, got %d", rec.Code)
	}
	if n := len(a.engine(dp).Lines()); n != 0 {
		t.Fatalf("A's basket changed: %d lines", n)
	}
	if rec := b.post("/api/self-order/scan", "code=5000001"); rec.Code != http.StatusOK {
		t.Fatalf("B must stay editable: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(b.engine(dp).Lines()); n != 1 {
		t.Fatalf("B's basket: %d lines, want 1", n)
	}
}
