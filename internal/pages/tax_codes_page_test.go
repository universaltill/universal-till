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
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// newTaxCodesTestDeps mirrors newPluginSettingsTestDeps (plugin_settings_page_test.go)
// -- same DB fixture, same Deps shape -- just mounting registerTaxCodes instead.
func newTaxCodesTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/plugins", Label: "Plugins"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
		AuthSvc:  auth.NewService(db),
	}
	mux := http.NewServeMux()
	registerTaxCodes(mux, dp)
	return mux, dp
}

func TestTaxCodesPage_GET_RequiresPermission(t *testing.T) {
	mux, _ := newTaxCodesTestDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/catalog/tax-codes", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a session, got %d", rec.Code)
	}
}

// Real-session role gating (ut-docs#259, same shape as ut-docs#706's
// TestPluginSettingsPages_RealSessionGatesByRole): manager/admin/super_admin
// get the tax_code_management action (057's migration), cashier doesn't.
func TestTaxCodesPage_RealSessionGatesByRole(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	for role, want := range map[string]int{
		"cashier": http.StatusForbidden, "manager": http.StatusOK,
		"admin": http.StatusOK, "super_admin": http.StatusOK,
	} {
		t.Run(role, func(t *testing.T) {
			mux, _ := newTaxCodesTestDeps(t)
			req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/catalog/tax-codes", nil), auth.User{ID: "u1", Role: role})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != want {
				t.Fatalf("GET role=%s: got %d, want %d", role, rec.Code, want)
			}
		})
	}
}

// Real-session role gating for the two WRITE endpoints, not just the GET
// page. Review finding (ut-docs#259): before this test existed, deleting the
// `canPerform` call from the POST /api/catalog/tax-codes/update handler
// entirely left every one of the eleven tax-code tests green -- the endpoint
// that rewrites a tax rate and retires a code had no gating coverage at all,
// only the read-only page did. Asserting per-role here (not merely the
// no-session 403 the *_RequiresPermission tests cover) is what makes a
// regression to "any signed-in cashier may edit tax rates" fail.
func TestTaxCodesAPI_RealSessionGatesByRole(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	endpoints := []struct {
		name string
		path string
		form string
	}{
		{"create", "/api/catalog/tax-codes", "name=Gated+Code&rate=11"},
		{"update", "/api/catalog/tax-codes/update", "id=tax_std&name=Standard&rate=20&isActive=1"},
	}
	for _, ep := range endpoints {
		for role, want := range map[string]int{
			"cashier": http.StatusForbidden, "manager": http.StatusOK,
			"admin": http.StatusOK, "super_admin": http.StatusOK,
		} {
			t.Run(ep.name+"/"+role, func(t *testing.T) {
				mux, dp := newTaxCodesTestDeps(t)
				req := httptest.NewRequest(http.MethodPost, ep.path, strings.NewReader(ep.form))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req = auth.WithUser(req, auth.User{ID: "u1", Role: role})
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code != want {
					t.Fatalf("POST %s role=%s: got %d, want %d (body %s)", ep.path, role, rec.Code, want, rec.Body.String())
				}
				if want != http.StatusForbidden {
					return
				}
				// A 403 must also mean nothing was written: a gate that
				// answers 403 after already having mutated the row would
				// still pass a status-only assertion.
				var n int
				if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM tax_codes WHERE name = 'Gated Code'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Fatalf("cashier's rejected create still wrote %d row(s)", n)
				}
			})
		}
	}
}

func TestTaxCodesPage_GET_RendersSeededActiveCode(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newTaxCodesTestDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/catalog/tax-codes", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// seedForPages seeds tax_std ("Standard", 2000bp, active).
	if !strings.Contains(body, "Standard") {
		t.Fatalf("expected seeded tax code in the page, got %s", body)
	}
	if !strings.Contains(body, `href="/plugins"`) {
		t.Fatalf("expected the manage-overrides link to /plugins, got %s", body)
	}
}

func TestTaxCodesAPI_Create_RequiresPermission(t *testing.T) {
	mux, _ := newTaxCodesTestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/tax-codes", strings.NewReader("name=X&rate=10"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a session, got %d", rec.Code)
	}
}

func TestTaxCodesAPI_Create_RoundTrip(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newTaxCodesTestDeps(t)

	form := "name=New+Rate+Code&rate=15&takeawayRate=7"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/tax-codes", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST create: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "New Rate Code") {
		t.Fatalf("expected the new tax code in the refreshed table, got %s", body)
	}
	if !strings.Contains(body, "15%") {
		t.Fatalf("expected the 15%% rate rendered, got %s", body)
	}
	if !strings.Contains(body, "7%") {
		t.Fatalf("expected the 7%% takeaway rate rendered, got %s", body)
	}
}

func TestTaxCodesAPI_Create_DuplicateNameRejected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newTaxCodesTestDeps(t)

	form := "name=Dup+Code&rate=10"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/tax-codes", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first create: code %d body %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/catalog/tax-codes", strings.NewReader(form))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("duplicate-name create: expected 400, got %d body %s", rec2.Code, rec2.Body.String())
	}
}

func TestTaxCodesAPI_Create_MalformedRateRejected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newTaxCodesTestDeps(t)

	// "100" is deliberately NOT in this list -- it is the inclusive upper
	// bound (see TestTaxCodesAPI_Create_AcceptsExactly100 below), matching
	// catimport.ParseTaxRateBP and the input's max="100". "100.01" is the
	// first rejected value above it.
	for _, bad := range []string{"abc", "-5", "NaN", "150", "1e300", "100.01"} {
		form := "name=Bad+Rate+" + bad + "&rate=" + bad
		req := httptest.NewRequest(http.MethodPost, "/api/catalog/tax-codes", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("rate=%q: expected 400, got %d body %s", bad, rec.Code, rec.Body.String())
		}
	}
}

// Review finding (ut-docs#259): the validator originally rejected `bp >=
// 10000`, so a rate of exactly 100% was refused -- while the input carries
// max="100", the error message says "between 0 and 100", and
// catimport.ParseTaxRateBP accepts 100, meaning a tax code the CSV importer
// creates happily could not be re-entered or re-saved by hand.
func TestTaxCodesAPI_Create_AcceptsExactly100(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newTaxCodesTestDeps(t)

	req := httptest.NewRequest(http.MethodPost, "/api/catalog/tax-codes",
		strings.NewReader("name=Full+Rate&rate=100&takeawayRate=100"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rate=100: expected 200, got %d body %s", rec.Code, rec.Body.String())
	}

	var rateBP, takeawayBP int
	if err := dp.Db.QueryRow(
		`SELECT rate_basis_points, takeaway_rate_basis_points FROM tax_codes WHERE name = 'Full Rate'`,
	).Scan(&rateBP, &takeawayBP); err != nil {
		t.Fatal(err)
	}
	if rateBP != 10000 || takeawayBP != 10000 {
		t.Fatalf("expected 10000bp/10000bp persisted, got %d/%d", rateBP, takeawayBP)
	}
}

// A blank/whitespace-only name must come back as the localised
// taxcodes.err.name_required, not a bare English literal: the page's JS
// renders the response body verbatim into its status line, and " " passes
// the browser's `required` attribute. Review finding, ut-docs#259.
func TestTaxCodesAPI_Create_BlankNameRejectedWithLocalisedMessage(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newTaxCodesTestDeps(t)

	req := httptest.NewRequest(http.MethodPost, "/api/catalog/tax-codes", strings.NewReader("name=+++&rate=10"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank name: expected 400, got %d body %s", rec.Code, rec.Body.String())
	}
	want := httpx.T("en", "taxcodes.err.name_required")
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("blank name: body %q, want the localised %q", got, want)
	}
}

func TestTaxCodesAPI_Update_RoundTrip(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newTaxCodesTestDeps(t)

	// Create, then read the id back off the rendered row.
	createForm := "name=Editable+Code&rate=12"
	createReq := httptest.NewRequest(http.MethodPost, "/api/catalog/tax-codes", strings.NewReader(createForm))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createRec := httptest.NewRecorder()
	mux.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create: code %d body %s", createRec.Code, createRec.Body.String())
	}
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM tax_codes WHERE name = 'Editable Code'`).Scan(&id); err != nil {
		t.Fatalf("find created id: %v", err)
	}

	updateForm := "id=" + id + "&name=Renamed+Code&rate=18&takeawayRate=&isActive=0"
	updateReq := httptest.NewRequest(http.MethodPost, "/api/catalog/tax-codes/update", strings.NewReader(updateForm))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateRec := httptest.NewRecorder()
	mux.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update: code %d body %s", updateRec.Code, updateRec.Body.String())
	}
	body := updateRec.Body.String()
	if !strings.Contains(body, "Renamed Code") {
		t.Fatalf("expected renamed code in refreshed table, got %s", body)
	}
	if !strings.Contains(body, "18%") {
		t.Fatalf("expected the 18%% rate rendered, got %s", body)
	}

	var name string
	var active int
	if err := dp.Db.QueryRow(`SELECT name, is_active FROM tax_codes WHERE id = ?`, id).Scan(&name, &active); err != nil {
		t.Fatalf("read back updated row: %v", err)
	}
	if name != "Renamed Code" {
		t.Fatalf("expected name Renamed Code, got %q", name)
	}
	if active != 0 {
		t.Fatalf("expected is_active=0 after the deactivate toggle, got %d", active)
	}
}

func TestTaxCodesAPI_Update_NotFound(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newTaxCodesTestDeps(t)
	form := "id=does-not-exist&name=X&rate=10"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/tax-codes/update", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestTaxCodesAPI_Update_MalformedRateRejected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newTaxCodesTestDeps(t)
	form := "id=tax_std&name=Standard&rate=abc"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/tax-codes/update", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", rec.Code, rec.Body.String())
	}
	// Nothing should have changed on the seeded row.
	var rateBP int
	if err := dp.Db.QueryRow(`SELECT rate_basis_points FROM tax_codes WHERE id = 'tax_std'`).Scan(&rateBP); err != nil {
		t.Fatal(err)
	}
	if rateBP != 2000 {
		t.Fatalf("expected tax_std's rate untouched at 2000, got %d", rateBP)
	}
}
