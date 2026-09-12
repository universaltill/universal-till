package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2154. menu.html's "Back to sale" link hard-linked to bare "/",
// the same mode-unaware shape ut-docs#2146 fixed for /open-orders — see
// saleScreenReturnURL's own doc comment (index_page.go) for why
// backoffice/self_order need opposite treatment: a manager who just tapped
// an explicit "Back to sale" link has opted out of the backoffice landing
// preference (so this bypasses it with "?stay=1"), while a self-order-mode
// till's containment (ADR-0020) is not something an explicit link can open
// a door around, so it stays on the till's own home instead of reaching the
// cashier screen.
func TestMenuPageBackToSaleURL_BackofficeModeReachesSaleScreenNotDashboard(t *testing.T) {
	mux, d := newMenuPageTestDeps(t, []common.MenuItem{})
	if err := d.Settings.Set(t.Context(), "display.mode", "backoffice"); err != nil {
		t.Fatalf("set display.mode: %v", err)
	}

	body := menuGet(t, mux)
	if !strings.Contains(body, `href="/?stay=1"`) {
		t.Fatalf("expected Back to sale to link to /?stay=1 on a backoffice-mode till, got: %s", body)
	}
}

func TestMenuPageBackToSaleURL_SelfOrderModeStaysOnTillHomeNotCashierScreen(t *testing.T) {
	mux, d := newMenuPageTestDeps(t, []common.MenuItem{})
	if err := d.Settings.Set(t.Context(), "display.mode", "self_order"); err != nil {
		t.Fatalf("set display.mode: %v", err)
	}

	body := menuGet(t, mux)
	if !strings.Contains(body, `href="/self-order"`) {
		t.Fatalf("expected Back to sale to link to /self-order on a self-order-mode till (ADR-0020 containment), got: %s", body)
	}
}

func TestMenuPageBackToSaleURL_DefaultModeIsUnchanged(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, []common.MenuItem{})

	body := menuGet(t, mux)
	if !strings.Contains(body, `href="/"`) || strings.Contains(body, `href="/?stay=1"`) {
		t.Fatalf("expected Back to sale to keep its plain / link on a register-mode till, got: %s", body)
	}
}

func menuGet(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /menu = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}
