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
		if ti < 0 || si < ti || (bi >= 0 && bi < ti && si > bi && si < bi+10) {
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
	cashier := cashier3134
	home := getIndexAs(t, "device", &cashier)
	if !strings.Contains(home, `data-testid="no-sale-open"`) {
		t.Fatal("a session without no_sale still sees the button (it gets the PIN prompt)")
	}
}
