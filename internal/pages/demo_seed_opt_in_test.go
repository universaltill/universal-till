package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// newRealDBDeps wires the setup + settings handlers over a REAL migrated
// database (post-036: no demo rows) — unlike newFullAuthDeps' minimal
// schema, this has the full catalogue tables the demo seed touches.
func newRealDBDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	initAuthTestI18n(t)
	dbo, err := db.Open(filepath.Join(t.TempDir(), "demo-optin.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	svc := auth.NewService(dbo.DB)
	store := settings.NewStore(dbo.DB)
	engine := pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	d := &common.Deps{
		Db:       dbo.DB,
		Settings: store,
		Engine:   engine,
		AuthSvc:  svc,
		Cfg:      &config.Config{Theme: "default"},
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
	}
	mux := http.NewServeMux()
	registerAuth(mux, d, svc)
	registerSetup(mux, d, svc)
	registerSettings(mux, d)
	return mux, d
}

// ut-docs#539: the wizard's new shop-type step persists shop.type, and the
// demo-data checkbox (default off) seeds the sample catalogue only when
// checked.
func TestSetupWizardShopTypeAndDemoOptIn(t *testing.T) {
	mux, d := newRealDBDeps(t)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":          {"2468"},
		"pin_confirm":  {"2468"},
		"country":      {"GB"},
		"currency":     {"GBP"},
		"tax_rate_pct": {"20"},
		"store_name":   {"Corner Shop"},
		"shop_type":    {"cafe"},
		"demo_data":    {"on"},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("wizard setup: code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if v, ok, _ := d.Settings.Get(t.Context(), common.KeyShopType); !ok || v != "cafe" {
		t.Fatalf("shop.type = %q ok=%v, want cafe", v, ok)
	}
	// The checkbox seeded the sample catalogue, flagged as such.
	seedRepo := data.NewDemoSeedRepo(d.Db)
	n, err := seedRepo.SampleItemCount(t.Context())
	if err != nil {
		t.Fatalf("SampleItemCount: %v", err)
	}
	if n != 50 {
		t.Fatalf("sample items after opt-in setup = %d, want 50", n)
	}
	// ut-docs#567: the same checkbox also seeds the demo customers/promos.
	custPromo, err := seedRepo.SampleCustomerPromoCount(t.Context())
	if err != nil {
		t.Fatalf("SampleCustomerPromoCount: %v", err)
	}
	if custPromo != 6 {
		t.Fatalf("sample customers/promos after opt-in setup = %d, want 6", custPromo)
	}
}

// Unchecked (the default): no sample data lands, and a shop type outside
// the ADR-0026 list is not persisted.
func TestSetupWizardNoDemoByDefaultAndInvalidShopTypeIgnored(t *testing.T) {
	mux, d := newRealDBDeps(t)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":         {"2468"},
		"pin_confirm": {"2468"},
		"country":     {"GB"},
		"currency":    {"GBP"},
		"store_name":  {"Corner Shop"},
		"shop_type":   {"unicorn_farm"},
		// demo_data intentionally absent — checkbox default is unchecked.
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if v, ok, _ := d.Settings.Get(t.Context(), common.KeyShopType); ok {
		t.Fatalf("invalid shop.type %q was persisted", v)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("items after default (unchecked) setup = %d, want 0", n)
	}
	// ut-docs#567: unchecked also leaves the demo customers/promos unseeded.
	for _, table := range []string{"customers", "promotions"} {
		if err := d.Db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s after default (unchecked) setup = %d, want 0", table, n)
		}
	}
}

// Offline-first reasoning extended to first boot: a failing demo seed must
// never block wizard completion. newFullAuthDeps' minimal schema has no
// catalogue tables at all, so the seed fails hard — setup must still finish.
func TestSetupWizardDemoSeedFailureDoesNotBlockSetup(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":         {"2468"},
		"pin_confirm": {"2468"},
		"country":     {"GB"},
		"currency":    {"GBP"},
		"store_name":  {"Corner Shop"},
		"demo_data":   {"on"},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("wizard setup with failing seed: code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if done, ok, _ := d.Settings.Get(t.Context(), "setup.completed"); !ok || done != "true" {
		t.Fatalf("setup.completed = %q ok=%v — a failing demo seed blocked setup", done, ok)
	}
}

// The wizard template carries the new step: shop-type select (all six
// ADR-0026 options) and the demo-data checkbox, unchecked by default.
func TestSetupWizardRendersShopTypeStep(t *testing.T) {
	// ut-docs#662: hermetic against the developer machine's real OS locale —
	// otherwise ut-docs#590's /setup detection redirect fires on any machine
	// with a locale set and this test never even reaches the wizard.
	withOSLocale(t, "", "")
	mux, _ := newRealDBDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`name="shop_type"`, `value="cafe"`, `value="retail"`, `value="service"`, `value="hospitality"`, `value="market_stall"`, `value="other"`, `name="demo_data"`} {
		if !strings.Contains(body, want) {
			t.Errorf("setup wizard page missing %s", want)
		}
	}
	if strings.Contains(body, `name="demo_data" checked`) {
		t.Error("demo-data checkbox must default to unchecked")
	}
}

// Settings: shop type is editable after setup (manager-gated, validated).
func TestSettingsShopTypeEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// Cashier: denied with the in-place elevation prompt (ut-docs#796),
	// and nothing written.
	rec := postForm(mux, "/api/settings/shop-type", url.Values{"shop_type": {"retail"}}, &cashUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier shop-type: code=%d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), common.KeyShopType); ok {
		t.Fatal("cashier's denied shop-type attempt must not have written shop.type")
	}

	// Manager, invalid value: rejected.
	rec = postForm(mux, "/api/settings/shop-type", url.Values{"shop_type": {"unicorn_farm"}}, &mgrUser)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid shop-type: code=%d, want 400", rec.Code)
	}

	// Manager, valid value: saved.
	rec = postForm(mux, "/api/settings/shop-type", url.Values{"shop_type": {"hospitality"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("manager shop-type: code=%d body=%s, want 204", rec.Code, rec.Body.String())
	}
	if v, ok, _ := d.Settings.Get(t.Context(), common.KeyShopType); !ok || v != "hospitality" {
		t.Fatalf("shop.type = %q ok=%v, want hospitality", v, ok)
	}
}

// Settings: "Remove sample data" runs the safe removal and reports how many
// items it removed vs couldn't remove (already sold/adjusted). Manager-only.
func TestSettingsRemoveDemoCatalogueEndpoint(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Sell one demo item so removal has to keep it.
	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s-1', 'R-1', 120, 120)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sale_lines
		(id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		VALUES ('sl-1', 's-1', 1, 'itm001', 'Coca-Cola Can 330ml', 1, 120, 2000, 20, 100, 120)`); err != nil {
		t.Fatal(err)
	}

	// Cashier: forbidden, nothing removed.
	rec := postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{}, &cashUser)
	if n, _ := repo.SampleItemCount(t.Context()); n != 50 {
		t.Fatalf("cashier attempt removed sample data (count=%d, code=%d)", n, rec.Code)
	}

	// Manager: removal runs and the response reports both counts.
	rec = postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager remove: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Tight on purpose (ut-docs#539 review, N4): a bare Contains(body, "1")
	// is trivially satisfied by the "1" inside "49" and asserts nothing.
	// "sample record" not "sample item" since ut-docs#567: the copy covers
	// customers/promo codes too now, not just catalogue items — this test's
	// DB only seeded the catalogue, so the combined removed/kept totals are
	// still exactly the catalogue's own 49/1.
	if !strings.Contains(body, "Removed 49 sample record") || !strings.Contains(body, "1 could not be removed") {
		t.Errorf("removal response %q does not report removed=49 kept=1", body)
	}
	if n, _ := repo.SampleItemCount(t.Context()); n != 1 {
		t.Fatalf("sample items after removal = %d, want 1 (the sold one)", n)
	}
}

// ut-docs#567: the Settings "Remove sample data" endpoint removes demo
// customers/promo codes too, not just the catalogue, and reports the
// combined removed/kept count across all three.
func TestSettingsRemoveDemoCatalogueEndpointCoversCustomersPromos(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCustomersPromos(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Sell to one demo customer so removal has to keep it.
	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, customer_id, subtotal, total) VALUES ('s-1', 'R-1', 'cust-001', 120, 120)`); err != nil {
		t.Fatal(err)
	}

	// Cashier: forbidden, nothing removed.
	postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{}, &cashUser)
	if n, _ := repo.SampleCustomerPromoCount(t.Context()); n != 6 {
		t.Fatalf("cashier attempt removed sample customers/promos (count=%d)", n)
	}

	// Manager: removal runs, response reports the combined count, and only
	// the touched customer survives.
	rec := postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager remove: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Catalogue (0 seeded here, so 0/0) + customers/promos (5 removed, 1
	// kept: cust-001 survives, cust-002/cust-003/PROMO50/PROMO500/DISC10 go).
	if !strings.Contains(body, "Removed 5 sample record") || !strings.Contains(body, "1 could not be removed") {
		t.Errorf("removal response %q does not report removed=5 kept=1", body)
	}
	if n, _ := repo.SampleCustomerPromoCount(t.Context()); n != 1 {
		t.Fatalf("sample customers/promos after removal = %d, want 1 (the sold-to customer)", n)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM customers WHERE id = 'cust-001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("sold-to demo customer cust-001 was removed")
	}
}

// ut-docs#633: a demo item LIVE in the current (not yet held) basket has no
// held_sales row for remove_demo.sql's own safety check to catch — the
// endpoint must refuse removal outright instead of letting a later tender
// FK-fail with no clear recovery.
func TestSettingsRemoveDemoCatalogueEndpoint_BlocksWhileDemoItemInLiveBasket(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	d.Engine.AddLineWithModifiers(pos.BasketLine{
		SKU: "SKU-0001", Name: "Coca-Cola Can 330ml", ItemID: "itm001", PriceCents: 120,
	}, 1, nil)

	rec := postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager remove: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "current basket") {
		t.Fatalf("removal response %q does not explain the live-basket block", rec.Body.String())
	}
	if n, _ := repo.SampleItemCount(t.Context()); n != 50 {
		t.Fatalf("sample items after blocked removal = %d, want 50 (removal must not have run)", n)
	}
}

// Same guard, exercised via the demo CUSTOMER set on the current basket
// rather than a basket line.
func TestSettingsRemoveDemoCatalogueEndpoint_BlocksWhileDemoCustomerInLiveBasket(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCustomersPromos(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	d.Engine.SetCustomer("cust-001", "Alice Carter")

	rec := postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager remove: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "current basket") {
		t.Fatalf("removal response %q does not explain the live-basket block", rec.Body.String())
	}
	if n, _ := repo.SampleCustomerPromoCount(t.Context()); n != 6 {
		t.Fatalf("sample customers/promos after blocked removal = %d, want 6 (removal must not have run)", n)
	}
}

// ut-docs#617: the "Restore from another POS" resume prompt only shows once
// the wizard's "Later" choice actually deferred it — not on a fresh till,
// and not once dismissed/resumed.
func TestSettingsShowsRestoreResumePromptOnlyWhenDeferred(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	getSettings := func() string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req = auth.WithUser(req, mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}

	if strings.Contains(getSettings(), `id="restore-resume-block"`) {
		t.Fatal("resume prompt shown before the wizard ever deferred it")
	}

	if err := d.Settings.Set(t.Context(), common.KeyRestorePromptStatus, common.RestorePromptStatusDeferred); err != nil {
		t.Fatal(err)
	}
	body := getSettings()
	if !strings.Contains(body, `id="restore-resume-block"`) {
		t.Fatal("resume prompt not shown once deferred")
	}
	if !strings.Contains(body, `href="/import"`) {
		t.Error("resume prompt does not link straight into /import")
	}
}

// Dismissing the resume prompt is manager-gated and clears the flag so it
// stops reappearing (distinct from actually using it via /import).
func TestSettingsDismissRestorePromptEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), common.KeyRestorePromptStatus, common.RestorePromptStatusDeferred); err != nil {
		t.Fatal(err)
	}

	// ut-docs#865: a denied cashier gets the in-place elevation prompt
	// (ut-docs#557/#796 mechanism), not a flat 403.
	rec := postForm(mux, "/api/settings/dismiss-restore-prompt", url.Values{}, &cashUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier dismiss: code=%d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), common.KeyRestorePromptStatus); v != common.RestorePromptStatusDeferred {
		t.Fatalf("cashier attempt cleared the flag: %q", v)
	}

	rec = postForm(mux, "/api/settings/dismiss-restore-prompt", url.Values{}, &mgrUser)
	// ut-docs#865 review finding F9: the plain-allowed path must stay a
	// bare empty body with no X-UT-Response header — that header is set
	// ONLY on the elevated path (see TestDismissRestorePrompt_ElevationFlow
	// in settings_elevation_test.go), so htmx's outerHTML removal on a
	// manager's own direct dismiss is unaffected by this card.
	if rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("manager dismiss set X-UT-Response = %q, want unset on the plain-allowed path", rec.Header().Get("X-UT-Response"))
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("manager dismiss: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if v, ok, _ := d.Settings.Get(t.Context(), common.KeyRestorePromptStatus); ok && v != "" {
		t.Fatalf("restore prompt status after dismiss = %q ok=%v, want cleared", v, ok)
	}
}
