package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6755 review F1: the shop's own terminal is never offered on, nor
// accepted by, the self-order kiosk.
func TestSelfOrderShop_NeverOffersCardExternal(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	addExternal(t, data.NewCardReaderRepo(dp.Db), "Counter")
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	do(http.MethodPost, "/api/self-order/scan", "code=5000001")
	rec := do(http.MethodGet, "/api/self-order/checkout", "")
	if strings.Contains(rec.Body.String(), "card_external") {
		t.Fatalf("kiosk picker offers card_external: %s", rec.Body.String())
	}
	rec = do(http.MethodPost, "/api/self-order/checkout", "method=card_external")
	if rec.Code == http.StatusOK {
		t.Fatalf("kiosk accepted card_external: %d", rec.Code)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM payments WHERE method_id='card_external'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("payments recorded: %d %v", n, err)
	}
}
