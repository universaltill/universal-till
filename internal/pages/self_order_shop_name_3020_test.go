package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3020 (folded into #3115): the self-order kiosk header reads the
// shop name live, like receipts do. It used to render d.Cfg.StoreName,
// loaded once at boot, so a rename in Settings, from my. or in the setup
// wizard showed only after a restart.
func TestSelfOrder_ShopNameIsReadLive(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	render := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /self-order = %d", rec.Code)
		}
		return rec.Body.String()
	}

	// Still the migration-001 placeholder: the boot-time name is the fallback.
	if body := render(); !strings.Contains(body, "Task Runner Cafe") {
		t.Fatalf("with store.name a placeholder the kiosk must show the boot name Task Runner Cafe")
	}
	if err := dp.Settings.Set(t.Context(), common.KeyStoreName, "Corner Bakery"); err != nil {
		t.Fatal(err)
	}
	body := render()
	if !strings.Contains(body, "Corner Bakery") {
		t.Fatalf("after a rename the kiosk must show Corner Bakery without a restart")
	}
	if strings.Contains(body, "Task Runner Cafe") {
		t.Fatalf("after a rename the kiosk still shows the boot-time name")
	}
}
