package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#368: the self-order kiosk shares the fail-closed tax rule with the
// cashier tender path — a basket line whose registered tax plugin is broken
// must not be sold at a silently-wrong base rate on the anonymous surface
// either. The kiosk answer is a translated inline error on the payment
// picker (never a modal), pointing the customer to the counter.
type kioskBlockedTaxAsker struct{}

func (kioskBlockedTaxAsker) AskTaxRateBP(l pos.BasketLine, orderType string) (int, bool, bool) {
	return 0, false, true
}

func TestSelfOrderShop_CheckoutFailsClosedOnBlockedTax(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	dp.KioskEngine.SetTaxRateAsker(kioskBlockedTaxAsker{})

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

	rec := post("/api/self-order/checkout", "method=card")
	if rec.Code != http.StatusConflict {
		t.Fatalf("blocked tax: want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	// The payment picker re-renders with the translated pointer to the
	// counter (an i18n key resolved by the template — never raw English on
	// this anonymous surface).
	if !strings.Contains(rec.Body.String(), "please pay at the counter") {
		t.Fatalf("expected the translated tax-unavailable message, got: %s", rec.Body.String())
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sales WHERE status = 'completed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a blocked kiosk checkout must record NO sale, got %d", n)
	}
	// The basket survives — staff can finish the order at the counter.
	if len(dp.KioskEngine.Basket().Lines) != 1 {
		t.Fatalf("kiosk basket must be untouched by the refusal, got %d lines", len(dp.KioskEngine.Basket().Lines))
	}
}
