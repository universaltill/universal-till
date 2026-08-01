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

// Self-order kiosk mode (ADR-0020): setting display.mode=self_order sends
// "/" to the kiosk landing page for any authenticated visitor. Mirrors
// TestBackofficeModeRedirectsHome.
func TestSelfOrderModeRedirectsHome(t *testing.T) {
	chdirRoot(t)
	t.Setenv("UT_AUTH", "off")
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
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerSettings(mux, dp)
	registerSelfOrder(mux, dp)

	home := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec
	}
	if rec := home(); rec.Code != http.StatusOK {
		t.Fatalf("register profile home = %d, want 200 sale screen", rec.Code)
	}

	form := strings.NewReader("mode=self_order")
	req := httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set self_order: %d %s", rec.Code, rec.Body.String())
	}

	if rec := home(); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/self-order" {
		t.Fatalf("self_order home = %d → %q, want 303 → /self-order", rec.Code, rec.Header().Get("Location"))
	}

	// The kiosk landing page itself renders.
	kioskRec := httptest.NewRecorder()
	mux.ServeHTTP(kioskRec, httptest.NewRequest(http.MethodGet, "/self-order", nil))
	if kioskRec.Code != http.StatusOK {
		t.Fatalf("kiosk landing = %d: %s", kioskRec.Code, kioskRec.Body.String())
	}
	body := kioskRec.Body.String()
	for _, want := range []string{"Welcome!", "Tap to start"} {
		if !strings.Contains(body, want) {
			t.Fatalf("kiosk landing missing %q", want)
		}
	}

	// Back to register: the sale screen returns.
	req = httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", strings.NewReader("mode=register"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set register: %d", rec.Code)
	}
	if rec := home(); rec.Code != http.StatusOK {
		t.Fatalf("register-again home = %d, want 200", rec.Code)
	}
}

// Unlike backoffice mode (which only redirects an already-manager session),
// self-order mode redirects EVERY authenticated session, including a plain
// cashier — a kiosk isn't meant to show the cashier screen to anyone by
// default once it's in self-order mode.
func TestSelfOrderModeRedirectsEverySession(t *testing.T) {
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
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerIndex(mux, dp)
	registerSettings(mux, dp)
	registerSelfOrder(mux, dp)

	mgr := &auth.User{ID: "mgr-1", Role: "manager"}
	setRec := httptest.NewRecorder()
	setReq := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", strings.NewReader("mode=self_order")), *mgr)
	setReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(setRec, setReq)
	if setRec.Code != http.StatusNoContent {
		t.Fatalf("manager set self_order mode: %d %s", setRec.Code, setRec.Body.String())
	}

	home := func(u *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if u != nil {
			req = auth.WithUser(req, *u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := home(mgr); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/self-order" {
		t.Fatalf("manager home = %d → %q, want 303 → /self-order", rec.Code, rec.Header().Get("Location"))
	}
	cashier := &auth.User{ID: "cashier-1", Role: "cashier"}
	if rec := home(cashier); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/self-order" {
		t.Fatalf("cashier home on a self_order-mode till = %d → %q, want 303 → /self-order (unlike backoffice mode)", rec.Code, rec.Header().Get("Location"))
	}
}

// Setting display.mode=self_order is manager/admin gated, same as backoffice.
func TestDisplayModeSelfOrderRequiresManagerRole(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerSettings(mux, dp)

	req := httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", strings.NewReader("mode=self_order"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "cashier-1", Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier setting self_order mode = %d, want 403", rec.Code)
	}
}

// The kiosk landing page itself is auth-exempt (internal/auth/middleware.go)
// and must render for a request with NO session at all — a real anonymous
// customer, not just an authenticated one who happened to get redirected.
func TestSelfOrderPage_ServesAnonymousRequest(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default", StoreName: "Task Runner Cafe"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}

	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)
	authSvc := auth.NewService(db)
	h := auth.Middleware(mux, authSvc)

	// No cookie, no auth.WithUser — a genuinely anonymous request through
	// the real middleware, not a bare mux.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous /self-order = %d, want 200 (must be auth-exempt)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Task Runner Cafe") {
		t.Errorf("kiosk landing missing shop name: %s", rec.Body.String())
	}
}

// ut-docs#208: the kiosk start screen has no way back to till settings
// today — a direct gap against universal-till/CLAUDE.md's offline-first
// "Status/lock/exit must always be reachable" rule. A discreet exit
// control must be present, linking to the existing /login flow (reusing
// the existing PIN-auth mechanism, not a new one).
func TestSelfOrderPage_HasPinGatedExitLink(t *testing.T) {
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
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerSelfOrder(mux, dp)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /self-order = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/login"`) {
		t.Fatalf("self-order landing missing an exit link to /login: %s", body)
	}
	if !strings.Contains(body, "selforder-exit") {
		t.Fatalf("self-order landing missing the discreet exit affordance styling: %s", body)
	}
}
