package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/catalog"
)

// ut-docs#4959: the self-order cart shows each line's item photo, like the
// cashier basket's .line-thumb (#4838) — the lines already carry ImageURL
// from the same resolver.
func TestSelfOrderCart_ShowsItemPhoto_4959(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedShopItem(t, d, "itm-tea", "TEA", "5000002", "Tea", 250)
	const photo = "/public/assets/items/itm-coffee/thumb.png"
	if err := data.NewCatalogRepo(d.DB).SetItemThumbnail(context.Background(), "itm-coffee", photo); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	post := func(body string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("scan %s: status %d: %s", body, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	post("code=5000002")
	body := post("code=5000001")
	if !strings.Contains(body, `class="selforder-cart-thumb" src="`+photo) {
		t.Fatalf("cart line for Flat White does not show its photo %s:\n%s", photo, body)
	}
	// A line with no picture keeps its column with an empty placeholder (rows
	// stay aligned) and never gets a broken <img>.
	if got := strings.Count(body, `class="selforder-cart-thumb selforder-cart-thumb-empty"`); got != 1 {
		t.Fatalf("want exactly one empty thumb placeholder (Tea), got %d:\n%s", got, body)
	}
	if strings.Contains(body, `class="selforder-cart-thumb" src=""`) {
		t.Fatalf("a line without a photo must not render an empty <img>:\n%s", body)
	}
}

// ut-docs#4959: a photo changed in the item editor while a customer's order
// is open reaches the kiosk basket and every table session's basket, not only
// the cashier's (#4838's RefreshBasketPhoto was cashier-only).
func TestRefreshBasketPhoto_ReachesKioskAndTableSessions_4959(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	repo := data.NewCatalogRepo(d.DB)

	if _, err := dp.KioskEngine.Scan("5000001"); err != nil {
		t.Fatal(err)
	}
	_, session, ok := dp.SelfOrderSessions.Create()
	if !ok {
		t.Fatal("create session")
	}
	if _, err := session.Scan("5000001"); err != nil {
		t.Fatal(err)
	}
	if got := dp.KioskEngine.Basket().Lines[0].ImageURL; got != "" {
		t.Fatalf("precondition: no photo yet, kiosk line has %q", got)
	}

	const photo = "/public/assets/items/itm-coffee/thumb.png"
	if err := repo.SetItemThumbnail(context.Background(), "itm-coffee", photo); err != nil {
		t.Fatal(err)
	}
	catalog.RefreshBasketPhoto(context.Background(), dp, repo, "itm-coffee")

	if got := dp.KioskEngine.Basket().Lines[0].ImageURL; got != photo {
		t.Errorf("kiosk basket line ImageURL = %q, want %q", got, photo)
	}
	if got := session.Basket().Lines[0].ImageURL; got != photo {
		t.Errorf("table session basket line ImageURL = %q, want %q", got, photo)
	}
}
