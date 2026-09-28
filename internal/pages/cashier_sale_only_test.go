package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#3079 (security, P1): a cashier is sale-only. Every route below
// rendered or accepted writes for ANY signed-in operator before this card;
// each must now refuse a cashier (and a request with no session) with 403
// and none of its page content, while a manager/admin/super_admin still
// gets through. The cashier role has no role_permissions rows at all, so
// this runs the REAL seeded catalog (migrations incl. 052), not a stub.
type saleOnlyRoute struct {
	method, path string
	// marker is page-body text a manager's response carries and a denied
	// response must not ("" for a fragment/API with no stable marker).
	marker string
	// managerOK lists the statuses a manager may get past the gate: the
	// POSTs send an empty body, so a manager reaches validation (400).
	managerOK []int
}

var saleOnlyRoutes = []saleOnlyRoute{
	{http.MethodGet, "/settings", `id="settings-shell"`, []int{http.StatusOK}},
	{http.MethodGet, "/reports", `id="report-tab-sales-trend"`, []int{http.StatusOK}},
	{http.MethodGet, "/ui/reports/tab/sales-trend", "", []int{http.StatusOK}},
	{http.MethodGet, "/inventory", `id="stock-levels-card"`, []int{http.StatusOK}},
	{http.MethodGet, "/ui/inventory/stock-table", `id="stock-table"`, []int{http.StatusOK}},
	{http.MethodGet, "/plugins", `id="plugin-manager"`, []int{http.StatusOK}},
	// No marketplace catalog is wired in this rig, so a manager gets the
	// handler's own 503 — i.e. past the gate.
	{http.MethodGet, "/plugins/store", "", []int{http.StatusOK, http.StatusServiceUnavailable}},
	{http.MethodPost, "/api/inventory/receipt", "", []int{http.StatusBadRequest}},
	// The override handler's own actor lookup refuses the synthetic
	// manager (no users row) with its own 403 "user not found" — past this
	// gate, which the message check below tells apart.
	{http.MethodPost, "/api/inventory/override", "", []int{http.StatusForbidden}},
	// Review of this card: the same data/writes, left ungated.
	{http.MethodGet, "/api/inventory/low-stock", "", []int{http.StatusOK}},
	{http.MethodPost, "/api/settings/theme", "", []int{http.StatusOK, http.StatusNoContent, http.StatusSeeOther}},
	{http.MethodGet, "/api/plugins/check-updates", "", []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusBadGateway}},
	{http.MethodGet, "/api/plugins/marketplace", "", []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusBadGateway}},
}

func newSaleOnlyMux(t *testing.T) *http.ServeMux {
	t.Helper()
	dp := newDesignerTestDeps(t)
	dp.Menu = baseMenu // production's boot-time tile list (init.go)
	t.Setenv("UT_AUTH", "on")
	httpx.InitRailVisibility(menuPredicateChecker(dp))
	t.Cleanup(func() { httpx.InitRailVisibility(nil) })
	mux := http.NewServeMux()
	registerSettings(mux, dp)
	registerReportsPage(mux, dp)
	registerInventoryPage(mux, dp)
	registerInventoryAPI(mux, dp)
	registerPluginsPage(mux, dp)
	registerPluginStore(mux, dp)
	registerMenu(mux, dp)
	registerPluginAPI(mux, dp)
	return mux
}

func saleOnlyDo(mux *http.ServeMux, rt saleOnlyRoute, user *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(rt.method, rt.path, nil)
	if rt.method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestCashierSaleOnly_RoutesRefuseCashier(t *testing.T) {
	mux := newSaleOnlyMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier"}
	for _, rt := range saleOnlyRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			for name, u := range map[string]*auth.User{"cashier": &cashier, "no session": nil} {
				rec := saleOnlyDo(mux, rt, u)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("%s %s %s = %d, want 403", name, rt.method, rt.path, rec.Code)
				}
				body := rec.Body.String()
				if !strings.Contains(body, "Manager or admin required") {
					t.Fatalf("%s's 403 on %s lacks the localized reason:\n%.600s", name, rt.path, body)
				}
				if rt.method == http.MethodGet && isWholePage(rt.path) &&
					!strings.Contains(body, "<h1>Not allowed</h1>") {
					t.Fatalf("%s's 403 page on %s must read as a refusal, not a failure:\n%.600s", name, rt.path, body)
				}
				if rt.marker != "" && strings.Contains(body, rt.marker) {
					t.Fatalf("%s's 403 on %s still carries page content %q", name, rt.path, rt.marker)
				}
				// A whole-page 403 keeps the rail (the cashier needs Sell),
				// and that rail has no Stock for them.
				if rt.method == http.MethodGet && isWholePage(rt.path) {
					if !strings.Contains(body, `data-testid="nav-till"`) {
						t.Fatalf("%s's 403 on %s has no Sell rail link", name, rt.path)
					}
					if strings.Contains(body, `href="/inventory"`) {
						t.Fatalf("%s's 403 on %s still links /inventory in the rail", name, rt.path)
					}
				}
			}
			for _, role := range []string{"manager", "admin", "super_admin"} {
				u := auth.User{ID: "u-" + role, Role: role}
				rec := saleOnlyDo(mux, rt, &u)
				ok := false
				for _, s := range rt.managerOK {
					ok = ok || rec.Code == s
				}
				if !ok {
					t.Fatalf("%s %s %s = %d, want one of %v:\n%.600s", role, rt.method, rt.path, rec.Code, rt.managerOK, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), "Manager or admin required") {
					t.Fatalf("%s %s %s was refused by the sale-only gate:\n%.600s", role, rt.method, rt.path, rec.Body.String())
				}
				if rt.marker != "" && !strings.Contains(rec.Body.String(), rt.marker) {
					t.Fatalf("%s %s lacks page marker %q", role, rt.path, rt.marker)
				}
			}
		})
	}
}

// The menu grid and the rail, through the real predicate wiring: a cashier
// sees neither Reports/Settings/Plugins tiles nor the Stock rail entry; a
// manager sees all four.
func TestCashierSaleOnly_MenuAndRail(t *testing.T) {
	mux := newSaleOnlyMux(t)
	get := func(u auth.User) string {
		rec := saleOnlyDo(mux, saleOnlyRoute{method: http.MethodGet, path: "/menu"}, &u)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s GET /menu = %d", u.Role, rec.Code)
		}
		return rec.Body.String()
	}
	cashier := get(auth.User{ID: "c1", Role: "cashier"})
	for _, href := range []string{`href="/reports"`, `href="/settings"`, `href="/plugins"`, `href="/inventory"`} {
		if strings.Contains(cashier, href) {
			t.Errorf("cashier /menu still contains %s", href)
		}
	}
	if !strings.Contains(cashier, `data-testid="nav-till"`) || !strings.Contains(cashier, `href="/journal"`) {
		t.Errorf("cashier /menu lost the sale flow (Sell rail / Journal tile)")
	}
	// ut-docs#3120: base.html's bug-report panel linked /my-reports for
	// everyone, but that page needs "reports" — a cashier's tap got a 403.
	if strings.Contains(cashier, `href="/my-reports"`) {
		t.Errorf("cashier bug-report panel still links /my-reports (403 for them)")
	}
	if !strings.Contains(cashier, `id="bugreport-panel"`) {
		t.Errorf("cashier lost the bug-report panel itself")
	}
	manager := get(auth.User{ID: "m1", Role: "manager"})
	for _, href := range []string{`href="/reports"`, `href="/settings"`, `href="/plugins"`, `href="/inventory"`, `href="/my-reports"`} {
		if !strings.Contains(manager, href) {
			t.Errorf("manager /menu is missing %s", href)
		}
	}
}

// The sale screen's phone-width Stock button (kiosk-inventory-link-phone)
// was unconditional; it now follows stock_management like the rail entry.
func TestCashierSaleOnly_SaleScreenPhoneStockLink(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	cfg := &config.Config{Theme: "default"}
	dp := &common.Deps{Cfg: cfg, Db: db, State: common.LoadState(t.Context(), settings.NewStore(db), cfg),
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db), AuthSvc: auth.NewService(db)}
	t.Setenv("UT_AUTH", "on")
	httpx.InitRailVisibility(menuPredicateChecker(dp))
	t.Cleanup(func() { httpx.InitRailVisibility(nil) })
	mux := http.NewServeMux()
	registerIndex(mux, dp)
	get := func(role string) string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), auth.User{ID: "u-" + role, Role: role})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s GET / = %d", role, rec.Code)
		}
		return rec.Body.String()
	}
	cashier := get("cashier")
	if strings.Contains(cashier, `kiosk-inventory-link-phone`) || strings.Contains(cashier, `href="/inventory"`) {
		t.Fatalf("cashier sale screen still links /inventory")
	}
	if !strings.Contains(cashier, `data-testid="kiosk-checkout-start-phone"`) {
		t.Fatalf("cashier sale screen lost the phone-width New sale button")
	}
	manager := get("manager")
	if !strings.Contains(manager, `data-testid="kiosk-inventory-link-phone"`) {
		t.Fatalf("manager sale screen lost the phone-width Stock link")
	}
}

// TestCashierSaleOnly_SellGridHasNoDesignerLink: the sell grid's "+ Add
// product" (empty state) and pencil (strip) both open /designer, which a
// cashier is refused -- neither may render for them (ut-docs#3079 UX pass).
// The jiggle badges stay (#2361: locked, they ask a manager to approve).
func TestCashierSaleOnly_SellGridHasNoDesignerLink(t *testing.T) {
	for _, withButton := range []bool{false, true} {
		for _, role := range []string{"cashier", "manager"} {
			mux, d := newButtonsMuxRealSession(t)
			if withButton {
				seedOneButton(t, d)
			}
			rec := getWithUser(mux, "/ui/buttons", &auth.User{ID: "u-" + role, Role: role})
			if rec.Code != http.StatusOK {
				t.Fatalf("%s /ui/buttons = %d", role, rec.Code)
			}
			links := strings.Contains(rec.Body.String(), `href="/designer"`)
			if want := role != "cashier"; links != want {
				t.Fatalf("withButton=%v %s: /designer link rendered=%v, want %v", withButton, role, links, want)
			}
		}
	}
}

// isWholePage: a GET that renders base.html (rail included), as opposed to
// an htmx fragment (/ui/) or a JSON/API route (/api/).
func isWholePage(path string) bool {
	return !strings.HasPrefix(path, "/ui/") && !strings.HasPrefix(path, "/api/")
}
