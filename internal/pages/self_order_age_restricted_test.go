package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#3340: the self-order kiosk shares the fail-closed shape of
// TestSelfOrderShop_CheckoutFailsClosedOnBlockedTax, keyed off
// items.age_restricted instead of a broken tax plugin: an unattended kiosk
// cannot check ID, so any restricted item in the basket refuses card
// checkout with a 409 and re-renders the payment picker in place with a
// translated pointer to the counter — the basket untouched so staff can
// finish the order at the till (kiosk PIN login -> till mode), where the
// cashier's ID-check prompt applies.
func TestSelfOrderShop_CheckoutFailsClosedOnAgeRestrictedItem(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedShopItem(t, d, "itm-cider", "CIDER", "5000002", "Cider", 450)
	seedStock(t, d, "itm-coffee", 10)
	seedStock(t, d, "itm-cider", 10)
	if _, err := d.DB.Exec(`UPDATE items SET age_restricted = 1 WHERE id = 'itm-cider'`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	post("/api/self-order/scan", "code=5000001")
	post("/api/self-order/scan", "code=5000002")
	if len(dp.KioskEngine.Basket().Lines) != 2 {
		t.Fatalf("setup: want 2 kiosk lines, got %d", len(dp.KioskEngine.Basket().Lines))
	}

	rec := post("/api/self-order/checkout", "method=card")
	if rec.Code != http.StatusConflict {
		t.Fatalf("age-restricted item: want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	// The payment picker re-renders with the translated pointer to the
	// counter (an i18n key resolved by the template — never raw English on
	// this anonymous surface).
	if !strings.Contains(rec.Body.String(), "Please pay at the counter — staff will need to check ID for this order.") {
		t.Fatalf("expected the translated age-check message, got: %s", rec.Body.String())
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sales WHERE status = 'completed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a blocked kiosk checkout must record NO sale, got %d", n)
	}
	// The basket survives — staff can finish the order at the counter.
	if len(dp.KioskEngine.Basket().Lines) != 2 {
		t.Fatalf("kiosk basket must be untouched by the refusal, got %d lines", len(dp.KioskEngine.Basket().Lines))
	}
}

// Control: the same kiosk with no restricted item still checks out — the
// refusal above is the flag's doing, not a broken fixture.
func TestSelfOrderShop_CheckoutUnrestrictedItemStillCompletes(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)
	req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	req = httptest.NewRequest(http.MethodPost, "/api/self-order/checkout", strings.NewReader("method=card"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusConflict {
		t.Fatalf("an unrestricted basket must not be refused: %s", rec.Body.String())
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sales WHERE status = 'completed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("control checkout should complete one sale, got %d (code %d: %s)", n, rec.Code, rec.Body.String())
	}
}
