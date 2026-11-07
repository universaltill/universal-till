package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#3560: the sale screen's No sale button (icon in the tender footer),
// shown only for a drawer-capable printer, never gated by the permission (a
// session without it gets the PIN prompt).

func getIndexAs(t *testing.T, mode string, u *auth.User) string {
	t.Helper()
	mux, d := newIndexAndButtonsMux(t)
	if mode != "" {
		setSettings(t, d, map[string]string{"printer.mode": mode})
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d: %.300s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestIndex_NoSaleButtonOnlyWithDrawerCapablePrinter(t *testing.T) {
	for _, mode := range []string{"device", "network"} {
		home := getIndexAs(t, mode, nil)
		for _, want := range []string{`data-testid="no-sale-open"`, `hx-post="/api/pos/no-sale"`, `hx-target="#no-sale-status"`, `id="no-sale-status"`} {
			if !strings.Contains(home, want) {
				t.Fatalf("mode %s: sale screen missing %q", mode, want)
			}
		}
		if strings.Contains(home, "no-sale-open-phone") {
			t.Fatalf("no phone duplicate (UX U2)")
		}
		// Status region sits inside .tender and outside #basket.
		ti := strings.Index(home, `class="tender"`)
		si := strings.Index(home, `id="no-sale-status"`)
		bi := strings.Index(home, `id="basket"`)
		// Tester (ut-docs#3560): also before the tender footer, so a status
		// region moved anywhere later in the page is caught.
		fi := strings.Index(home, `class="tender-default-footer`)
		if ti < 0 || si < ti || fi < 0 || si > fi || (bi >= 0 && bi < ti && si > bi && si < bi+10) {
			t.Fatalf("mode %s: #no-sale-status must be inside .tender", mode)
		}
	}
	for _, mode := range []string{"", "off", "system", "junk"} {
		home := getIndexAs(t, mode, nil)
		if strings.Contains(home, `data-testid="no-sale-open"`) {
			t.Fatalf("mode %q: button must be hidden", mode)
		}
	}
}

func TestIndex_NoSaleButtonShownToCashierWithoutPermission(t *testing.T) {
	// Tester (ut-docs#3560): newButtonsMux runs with UT_AUTH=off, where
	// canPerform is always true, so without auth on this could not tell a
	// permission-gated button from an ungated one. Turn auth on with a real
	// AuthSvc and confirm the cashier really lacks no_sale.
	mux, d := newIndexAndButtonsMux(t)
	t.Setenv("UT_AUTH", "on")
	d.AuthSvc = auth.NewService(d.Db)
	setSettings(t, d, map[string]string{"printer.mode": "device"})
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), cashier3134)
	if canPerform(d, req, "no_sale") {
		t.Fatal("fixture: cashier must not hold no_sale")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d: %.300s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `data-testid="no-sale-open"`) {
		t.Fatal("a session without no_sale still sees the button (it gets the PIN prompt)")
	}
}

// AC10 (tester, ut-docs#3560): the self-order kiosk's customer page never
// renders the No sale button, even with a drawer-capable printer.
func TestSelfOrder_NeverRendersNoSaleButton(t *testing.T) {
	dp, _ := setupSelfOrderShopDeps(t)
	if err := dp.Settings.Set(t.Context(), "printer.mode", "device"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order = %d", rec.Code)
	}
	for _, bad := range []string{"no-sale-open", "/api/pos/no-sale", "no-sale-status"} {
		if strings.Contains(rec.Body.String(), bad) {
			t.Fatalf("/self-order renders %q", bad)
		}
	}
}
