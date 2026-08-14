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

// Back-office mode (ADR-0018): a manager station's home is the reports page —
// the sale screen is unreachable. Default profile keeps the sale screen.
func TestBackofficeModeRedirectsHome(t *testing.T) {
	chdirRoot(t)
	// /backoffice is manager/admin role-gated; UT_AUTH=off is the documented
	// no-session bypass for dev/CI tooling (isManagerOrAuthOff).
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
	registerBackofficePage(mux, dp)

	home := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec
	}
	if rec := home(); rec.Code != http.StatusOK {
		t.Fatalf("register profile home = %d, want 200 sale screen", rec.Code)
	}

	form := strings.NewReader("mode=backoffice")
	req := httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set backoffice: %d %s", rec.Code, rec.Body.String())
	}

	if rec := home(); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/backoffice" {
		t.Fatalf("backoffice home = %d → %q, want 303 → /backoffice", rec.Code, rec.Header().Get("Location"))
	}

	// The dashboard itself renders: KPI cards + low-stock + problems sections.
	dashRec := httptest.NewRecorder()
	mux.ServeHTTP(dashRec, httptest.NewRequest(http.MethodGet, "/backoffice", nil))
	if dashRec.Code != http.StatusOK {
		t.Fatalf("dashboard = %d: %s", dashRec.Code, dashRec.Body.String())
	}
	body := dashRec.Body.String()
	for _, want := range []string{"Back office", "Today", "Low stock", "Recent problems"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard missing %q", want)
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

// /backoffice is reachable from ANY till by URL (ADR-0011 single source of
// truth stays the primary/replica database; backoffice is a role gate, not a
// device lock) — but only for a manager/admin operator. A cashier is refused;
// a manager gets the dashboard.
func TestBackofficeRequiresManagerRole(t *testing.T) {
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
	// AuthSvc (ut-docs#713): /backoffice is now canPerform()-gated, which
	// queries role_permissions for real via AuthSvc.Can() — seedForPages
	// already seeds it (manager/admin/super_admin granted "reports").
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerBackofficePage(mux, dp)

	get := func(u *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/backoffice", nil)
		if u != nil {
			req = auth.WithUser(req, *u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := get(nil); rec.Code != http.StatusForbidden {
		t.Fatalf("no session = %d, want 403", rec.Code)
	}
	if rec := get(&auth.User{ID: "cashier-1", Role: "cashier"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403", rec.Code)
	}
	if rec := get(&auth.User{ID: "mgr-1", Role: "manager"}); rec.Code != http.StatusOK {
		t.Fatalf("manager = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec := get(&auth.User{ID: "admin-1", Role: "admin"}); rec.Code != http.StatusOK {
		t.Fatalf("admin = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// super_admin broadens vs. isManagerOrAuthOff (which only recognized
	// manager/admin) — accepted per #555, and worth pinning explicitly so a
	// regression back to the old gate doesn't silently pass this test (it
	// would, on manager/admin alone).
	if rec := get(&auth.User{ID: "super-1", Role: "super_admin"}); rec.Code != http.StatusOK {
		t.Fatalf("super_admin = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// Setting display.mode=backoffice is itself manager/admin gated — a cashier
// can't flip their own till's default landing page.
func TestDisplayModeBackofficeRequiresManagerRole(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	// AuthSvc (ut-docs#710): display-mode is now canPerform()-gated, which
	// queries role_permissions for real via AuthSvc.Can() — seedForPages
	// already seeds it (manager/admin/super_admin granted "settings").
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerSettings(mux, dp)

	setMode := func(u *auth.User, mode string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", strings.NewReader("mode="+mode))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if u != nil {
			req = auth.WithUser(req, *u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := setMode(&auth.User{ID: "cashier-1", Role: "cashier"}, "backoffice"); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier setting backoffice mode = %d, want 403", rec.Code)
	}
	if rec := setMode(&auth.User{ID: "mgr-1", Role: "manager"}, "backoffice"); rec.Code != http.StatusNoContent {
		t.Fatalf("manager setting backoffice mode = %d, want 204: %s", rec.Code, rec.Body.String())
	}
}

// The self-lockout regression a review caught: once a manager sets
// display.mode=backoffice, a CASHIER session at that same till must still
// land on the normal sale screen (not a dead-end 403) — the mode is a
// default-landing preference for managers, not a role bypass that strands
// other operators.
func TestBackofficeModeFallsThroughForNonManagerSession(t *testing.T) {
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
	// AuthSvc (ut-docs#710): display-mode is now canPerform()-gated, which
	// queries role_permissions for real via AuthSvc.Can() — seedForPages
	// already seeds it (manager/admin/super_admin granted "settings").
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

	home := func(u *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if u != nil {
			req = auth.WithUser(req, *u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := home(mgr); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/backoffice" {
		t.Fatalf("manager home = %d → %q, want 303 → /backoffice", rec.Code, rec.Header().Get("Location"))
	}
	cashier := &auth.User{ID: "cashier-1", Role: "cashier"}
	if rec := home(cashier); rec.Code != http.StatusOK {
		t.Fatalf("cashier home on a backoffice-mode till = %d, want 200 sale screen (not a dead-end)", rec.Code)
	}
	// The "/" redirect gate is canPerform(d, r, "reports") as of ut-docs#713,
	// and super_admin is exactly what distinguishes it from the old
	// isManagerOrAuthOff (manager/admin only, per #555). Without this case the
	// manager/cashier pair above passes identically under either gate, so a
	// regression here would go unnoticed (review, ut-docs#713).
	super := &auth.User{ID: "super-1", Role: "super_admin"}
	if rec := home(super); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/backoffice" {
		t.Fatalf("super_admin home = %d → %q, want 303 → /backoffice", rec.Code, rec.Header().Get("Location"))
	}
}
