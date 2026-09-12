package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#2146. /open-orders's own "Back to sale" link plain-linked to bare
// "/" -- which re-applies whatever display.mode landing preference is set,
// not necessarily the sale screen. Split out of the ut-docs#2138 review
// (universaltill/universal-till#1103's code-review record, finding N1),
// which found the same shape on the (not yet merged) resume redirect this
// page is meant to gain -- saleScreenReturnURL (index_page.go) is the shared
// helper both are meant to use.
//
// The two modes need OPPOSITE treatment, not a single bypass:
//
//   - backoffice (ADR-0018): registerIndex's own comment already documents
//     that redirect as "a default landing preference, not a role bypass" --
//     an operator who just tapped an explicit "back to sale" link has
//     already opted out of that default, so this bypasses it.
//   - self_order (ADR-0020): the opposite call, deliberately. That redirect
//     is kiosk containment, not a preference -- registerIndex's own comment
//     says it applies to "every authenticated session ... since a
//     self-order-mode till isn't meant to show the cashier screen to anyone
//     by default." An explicit link can't open a door ADR-0020 says should
//     not exist, so this test pins that "Back to sale" on a self-order-mode
//     till still resolves to /self-order, NOT the cashier sale screen --
//     accepting the till's own home instead of bypassing containment.
func TestOpenOrdersBackToSaleURL_BackofficeModeReachesSaleScreenNotDashboard(t *testing.T) {
	mux, d := newOpenOrdersFullFixtureMux(t)
	if err := d.Settings.Set(t.Context(), "display.mode", "backoffice"); err != nil {
		t.Fatalf("set display.mode: %v", err)
	}

	body := openOrdersGet(t, mux)
	if !strings.Contains(body, `href="/?stay=1"`) {
		t.Fatalf("expected Back to sale to link to /?stay=1 on a backoffice-mode till, got: %s", body)
	}
}

// newOpenOrdersFullFixtureMux is newOpenOrdersTestMux's counterpart for the
// two mode-redirect tests below: they only need a real `settings` table
// (display.mode), which newOpenOrdersTestMux's minimal held-sales schema
// doesn't carry (it's built for the held-sales/tables read path only).
func newOpenOrdersFullFixtureMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerOpenOrders(mux, dp)
	return mux, dp
}

// TestStayParamBypassesBackofficeRedirect pins the other half of the fix:
// the "stay=1" link target (saleScreenReturnURL's own choice for backoffice
// mode) must actually reach the sale screen, not just be the string this
// page happens to link to. Uses the same full seeded fixture
// backoffice_mode_test.go's own tests do -- registerIndex's handler queries
// payment methods, which newOpenOrdersTestMux's minimal held-sales schema
// doesn't carry.
func TestStayParamBypassesBackofficeRedirect(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerSettings(mux, dp)
	registerBackofficePage(mux, dp)

	mgr := &auth.User{ID: "mgr-1", Role: "manager"}
	setRec := httptest.NewRecorder()
	setReq := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", strings.NewReader("mode=backoffice")), *mgr)
	setReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(setRec, setReq)
	if setRec.Code != http.StatusNoContent {
		t.Fatalf("manager set backoffice mode: %d %s", setRec.Code, setRec.Body.String())
	}

	home := func(u *auth.User, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if u != nil {
			req = auth.WithUser(req, *u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Baseline, matching TestBackofficeModeFallsThroughForNonManagerSession:
	// a manager hitting plain "/" still gets bounced to /backoffice.
	if rec := home(mgr, "/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/backoffice" {
		t.Fatalf("manager GET / = %d -> %q, want 303 -> /backoffice", rec.Code, rec.Header().Get("Location"))
	}
	// The fix: the SAME manager, hitting the "stay=1" URL saleScreenReturnURL
	// hands out, reaches the sale screen instead.
	if rec := home(mgr, "/?stay=1"); rec.Code != http.StatusOK {
		t.Fatalf("manager GET /?stay=1 = %d, want 200 sale screen (not a redirect): %s", rec.Code, rec.Body.String())
	}
}

func TestOpenOrdersBackToSaleURL_SelfOrderModeStaysOnTillHomeNotCashierScreen(t *testing.T) {
	mux, d := newOpenOrdersFullFixtureMux(t)
	if err := d.Settings.Set(t.Context(), "display.mode", "self_order"); err != nil {
		t.Fatalf("set display.mode: %v", err)
	}

	body := openOrdersGet(t, mux)
	if !strings.Contains(body, `href="/self-order"`) {
		t.Fatalf("expected Back to sale to link to /self-order on a self-order-mode till (ADR-0020 containment), got: %s", body)
	}
}

func TestOpenOrdersBackToSaleURL_DefaultModeIsUnchanged(t *testing.T) {
	mux, _ := newOpenOrdersTestMux(t)
	body := openOrdersGet(t, mux)
	if !strings.Contains(body, `href="/"`) || strings.Contains(body, `href="/?stay=1"`) {
		t.Fatalf("expected Back to sale to keep its plain / link on a register-mode till, got: %s", body)
	}
}

// Unit-level pin on the shared helper itself, independent of the template
// wiring above (covers a mode this page's own test mux setup doesn't reach).
func TestSaleScreenReturnURL(t *testing.T) {
	cases := map[string]string{
		"":           "/",
		"register":   "/",
		"backoffice": "/?stay=1",
		"self_order": "/self-order",
	}
	for mode, want := range cases {
		if got := saleScreenReturnURL(mode); got != want {
			t.Errorf("saleScreenReturnURL(%q) = %q, want %q", mode, got, want)
		}
	}
}
