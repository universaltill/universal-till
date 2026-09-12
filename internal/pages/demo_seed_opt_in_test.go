package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// realDBTemplate* hold the one fully-migrated SQLite file this test binary
// builds, as raw bytes, so every newRealDBDeps caller can clone it instead
// of running the whole migration chain again (ut-docs#2191).
//
// Why: `go test ./internal/pages/... -race` was timing out at the default
// 600s, and the blame landed on whichever test happened to be running when
// the deadline fired — not a deadlock, just ~80 from-scratch db.Open calls
// (48 of them through newRealDBDeps alone) each executing every migration's
// DDL and seed SQL under -race instrumentation. The template is built once
// per process; each test still gets its own private copy under its own
// t.TempDir(), so nothing a test writes can leak into another.
//
// What this relies on in internal/db (do not break it without revisiting
// this helper): db.Open's migrate() treats a database whose
// schema_migrations ledger is already at the newest version as "nothing to
// apply" — it still runs verifyAppliedMigrations (a few ledger SELECTs plus a
// checksum compare per migration file, cheap), so a migration file edited or
// renumbered after the template was built is STILL caught, on the very next
// clone. No production code changes; if migrate() ever stops being cheap on
// an already-migrated file (say, an unconditional re-run step), this helper
// stops paying for itself and the isolation test below is where that shows.
//
// The build is deliberately NOT under a t.TempDir(): that is scoped to
// whichever test trips the sync.Once and is removed when that test ends, so
// the bytes are read eagerly into memory and the throwaway directory is
// removed inside the Once, before anything else can depend on it. The DSN
// opens in WAL mode, so the checkpoint below folds any outstanding -wal
// content into the main file before it is read — without it the copy would
// be missing whatever the last migrations wrote.
var (
	realDBTemplateOnce  sync.Once
	realDBTemplateBytes []byte
	realDBTemplateErr   error
)

// realDBTemplate returns the cached fully-migrated database file bytes,
// building them on the first call. A build failure is recorded once and
// reported through t.Fatalf by every caller, so it surfaces on the test that
// asked rather than as a panic from whichever goroutine happened to win the
// Once.
func realDBTemplate(t *testing.T) []byte {
	t.Helper()
	realDBTemplateOnce.Do(func() {
		realDBTemplateBytes, realDBTemplateErr = buildRealDBTemplate()
	})
	if realDBTemplateErr != nil {
		t.Fatalf("build migrated template db (ut-docs#2191): %v", realDBTemplateErr)
	}
	return realDBTemplateBytes
}

// buildRealDBTemplate runs the real migration chain exactly once, into a
// throwaway directory it removes before returning, and hands back the
// resulting single-file database.
func buildRealDBTemplate() ([]byte, error) {
	dir, err := os.MkdirTemp("", "ut-pages-dbtemplate-")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "template.db")
	dbo, err := db.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open+migrate: %w", err)
	}
	// Fold the WAL into the main file so the bytes we copy are the complete
	// migrated state (journal_mode=WAL in db.Open's DSN).
	if _, err := dbo.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = dbo.Close()
		return nil, fmt.Errorf("wal checkpoint: %w", err)
	}
	if err := dbo.Close(); err != nil {
		return nil, fmt.Errorf("close: %w", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read template: %w", err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("template file %s is empty after migration", path)
	}
	return b, nil
}

// newRealDBDeps wires the setup + settings handlers over a REAL migrated
// database (post-036: no demo rows) — unlike newFullAuthDeps' minimal
// schema, this has the full catalogue tables the demo seed touches.
//
// The database is a per-test clone of the once-built template (see
// realDBTemplate, ut-docs#2191): copied to a fresh path under THIS test's
// t.TempDir() and then opened through the normal db.Open, whose migrate()
// finds the ledger already complete and only verifies it.
func newRealDBDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	initAuthTestI18n(t)
	path := filepath.Join(t.TempDir(), "demo-optin.db")
	if err := os.WriteFile(path, realDBTemplate(t), 0o600); err != nil {
		t.Fatalf("clone template db: %v", err)
	}
	dbo, err := db.Open(path)
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

// The wizard template carries the new step: shop-type tile picker (all six
// ADR-0026 options, ut-docs#1095 — was a native <select>, now touch-first
// tiles carrying the type in data-value rather than an <option>'s value) and
// the demo-data checkbox, unchecked by default.
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
	for _, want := range []string{`name="shop_type"`, `data-value="cafe"`, `data-value="retail"`, `data-value="service"`, `data-value="hospitality"`, `data-value="market_stall"`, `data-value="other"`, `name="demo_data"`} {
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
	// DB only seeded the catalogue, so the removed total is still exactly
	// the catalogue's own 49. ut-docs#1840: the single "N could not be
	// removed" line is now a per-item list, named-and-reasoned — itm001 was
	// genuinely sold, so it's kept with the "history" reason, not the old
	// blanket "already in use" text.
	if !strings.Contains(body, "Removed 49 sample record") {
		t.Errorf("removal response %q does not report removed=49", body)
	}
	if !strings.Contains(body, "could not be removed automatically") || !strings.Contains(body, "Coca-Cola Can 330ml") ||
		!strings.Contains(body, "real trading history") {
		t.Errorf("removal response %q does not name+reason the kept item", body)
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
	// ut-docs#1858: the old blanket "N customer/promo record(s) could not be
	// removed (already in use)" line is gone — cust-001 now gets its own
	// named-and-reasoned row, same treatment catalogue items already got
	// from #1840.
	if !strings.Contains(body, "Removed 5 sample record") {
		t.Errorf("removal response %q does not report removed=5", body)
	}
	if !strings.Contains(body, "Alice Carter") || !strings.Contains(body, "sales on record") {
		t.Errorf("removal response %q does not name+reason the kept customer", body)
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

// ut-docs#746: the guard checks d.KioskEngine too (ADR-0020: a separate
// basket from the cashier's), but until now only the nil-skip path for it
// was ever exercised — no test proved the positive case, an item actually
// live in the self-order kiosk's basket, blocks removal. It must also
// name the kiosk specifically, not the generic cashier message.
func TestSettingsRemoveDemoCatalogueEndpoint_BlocksWhileDemoItemInKioskLiveBasket(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	d.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	d.KioskEngine.AddLineWithModifiers(pos.BasketLine{
		SKU: "SKU-0001", Name: "Coca-Cola Can 330ml", ItemID: "itm001", PriceCents: 120,
	}, 1, nil)

	rec := postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager remove: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "kiosk") {
		t.Fatalf("removal response %q does not name the kiosk basket specifically", body)
	}
	if strings.Contains(body, "current basket") {
		t.Fatalf("removal response %q reads as the cashier message, not the kiosk-specific one", body)
	}
	if n, _ := repo.SampleItemCount(t.Context()); n != 50 {
		t.Fatalf("sample items after blocked removal = %d, want 50 (removal must not have run)", n)
	}
}

// ut-docs#746: demoDataInLiveBasket checks cashier before kiosk in a fixed
// order specifically so a simultaneous match on both baskets resolves
// deterministically (documented on the function) — this pins that down,
// not just the two baskets independently.
func TestSettingsRemoveDemoCatalogueEndpoint_BothBasketsLiveReportsCashierFirst(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	d.Engine.AddLineWithModifiers(pos.BasketLine{
		SKU: "SKU-0001", Name: "Coca-Cola Can 330ml", ItemID: "itm001", PriceCents: 120,
	}, 1, nil)
	d.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	d.KioskEngine.AddLineWithModifiers(pos.BasketLine{
		SKU: "SKU-0001", Name: "Coca-Cola Can 330ml", ItemID: "itm001", PriceCents: 120,
	}, 1, nil)

	rec := postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager remove: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "current basket") || strings.Contains(body, "kiosk") {
		t.Fatalf("removal response %q does not report the cashier basket first, as documented", body)
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

// ut-docs#1840 AC3: "remove anyway" for a demo item kept only because it
// was edited. Needs real trading history elsewhere in the till (via a real,
// non-sample item) so the bulk removal itself doesn't already remove the
// edited item, leaving nothing for this per-item endpoint to demonstrate.
func TestSettingsRemoveDemoItemEndpoint(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO items (id, name, base_price) VALUES ('own-1', 'My Own Item', 250)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s-1', 'R-1', 250, 250)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sale_lines
		(id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		VALUES ('sl-1', 's-1', 1, 'own-1', 'My Own Item', 1, 250, 0, 0, 250, 250)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`UPDATE items SET name = 'Flat White' WHERE id = 'itm001'`); err != nil {
		t.Fatal(err)
	}

	// Cashier: forbidden, item still there.
	postForm(mux, "/api/settings/demo-item/itm001/remove", url.Values{}, &cashUser)
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("cashier's request removed itm001")
	}

	// Manager: removed, and the response confirms it.
	rec := postForm(mux, "/api/settings/demo-item/itm001/remove", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Removed") {
		t.Fatalf("manager remove-anyway: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("itm001 survived the manager's remove-anyway request")
	}
}

// The server-side re-check refuses an item that actually has trading
// history, regardless of what the client believed when it rendered the
// button — same non-negotiable as the bulk endpoint's own basket check.
func TestSettingsRemoveDemoItemEndpoint_RefusesItemWithHistory(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s-1', 'R-1', 120, 120)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sale_lines
		(id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		VALUES ('sl-1', 's-1', 1, 'itm001', 'Coca-Cola Can 330ml', 1, 120, 2000, 20, 100, 120)`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/settings/demo-item/itm001/remove", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("remove-anyway on a sold item: code=%d body=%s, want a refusal", rec.Code, rec.Body.String())
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm001'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("itm001 was removed despite having real sale history")
	}
}

// ut-docs#1840 AC3's other resolution: "keep as my own item".
func TestSettingsKeepDemoItemEndpoint(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.Db.Exec(`UPDATE items SET name = 'Flat White' WHERE id = 'itm001'`); err != nil {
		t.Fatal(err)
	}

	// Cashier: forbidden, still flagged sample.
	postForm(mux, "/api/settings/demo-item/itm001/keep", url.Values{}, &cashUser)
	var flagged int
	if err := d.Db.QueryRow(`SELECT is_sample_data FROM items WHERE id = 'itm001'`).Scan(&flagged); err != nil {
		t.Fatal(err)
	}
	if flagged != 1 {
		t.Fatal("cashier's request cleared is_sample_data")
	}

	rec := postForm(mux, "/api/settings/demo-item/itm001/keep", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Kept") {
		t.Fatalf("manager keep-as-own: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := d.Db.QueryRow(`SELECT is_sample_data FROM items WHERE id = 'itm001'`).Scan(&flagged); err != nil {
		t.Fatal(err)
	}
	if flagged != 0 {
		t.Fatal("itm001 still flagged is_sample_data after keep-as-own")
	}
}

// ut-docs#1840 review finding F4: the KeptReasonEdited branch of the kept
// list — its two buttons, their hx-post URLs, and the shared message span
// id both must reference — was previously exercised by no test at all.
// This is exactly the ut-docs#865 F1 invariant the diff's own code comment
// claims to preserve, so assert it directly rather than trusting the
// comment.
func TestSettingsRemoveDemoCatalogueEndpoint_EditedItemRowHasBothButtons(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Real trading history elsewhere forces strict mode, so the edited item
	// is KEPT (not removed outright) and shows up in the rendered list.
	if _, err := d.Db.Exec(`INSERT INTO items (id, name, base_price) VALUES ('own-1', 'My Own Item', 250)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s-1', 'R-1', 250, 250)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sale_lines
		(id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		VALUES ('sl-1', 's-1', 1, 'own-1', 'My Own Item', 1, 250, 0, 0, 250, 250)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`UPDATE items SET name = 'Flat White' WHERE id = 'itm001'`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager remove: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`hx-post="/api/settings/demo-item/itm001/remove"`,
		`hx-post="/api/settings/demo-item/itm001/keep"`,
		`hx-target="[id=&#34;demo-item-msg-itm001&#34;]"`,
		`id="demo-item-msg-itm001"`,
		"Flat White",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("edited-item row missing %q\nbody=%s", want, body)
		}
	}
}

// ut-docs#1840 review finding F3: an unknown/already-gone item id must be
// refused BEFORE checkOrElevate — a cashier hitting either per-item
// endpoint with a bogus id should get a plain not-found, never a manager-PIN
// prompt for an action that was always going to be a no-op.
func TestSettingsDemoItemEndpoints_UnknownIDNeverElevates(t *testing.T) {
	mux, d := newRealDBDeps(t)
	if err := data.NewDemoSeedRepo(d.Db).SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, path := range []string{
		"/api/settings/demo-item/does-not-exist/remove",
		"/api/settings/demo-item/does-not-exist/keep",
		"/api/settings/demo-promo/DOES-NOT-EXIST/remove",
		"/api/settings/demo-promo/DOES-NOT-EXIST/keep",
	} {
		rec := postForm(mux, path, url.Values{}, &cashUser)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "elevation-dialog") {
			t.Fatalf("%s: code=%d body=%s, want a plain refusal with no elevation prompt", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "error") {
			t.Fatalf("%s: body=%s, want an error span", path, rec.Body.String())
		}
	}
}

// ut-docs#1840 review finding F2: the elevated success path must NOT set
// X-UT-Response: ok — that header tells elevation_prompt.html's retry form
// to reload the whole page, which would wipe out every OTHER kept row the
// merchant hasn't resolved yet. Mirrors remove-demo-catalogue's own bulk
// handler, which never sets it either.
func TestSettingsRemoveDemoItemEndpoint_ElevatedSuccessDoesNotSetReloadHeader(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mgrID, cashierID := seedElevationUsers(t, d)
	_ = mgrID
	cashier := auth.User{ID: cashierID, Role: "cashier"}
	if _, err := d.Db.Exec(`UPDATE items SET name = 'Flat White' WHERE id = 'itm001'`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/settings/demo-item/itm001/keep", url.Values{"override_pin": {"555222"}}, &cashier)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Kept") {
		t.Fatalf("elevated keep-as-own: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("elevated keep-as-own set X-UT-Response = %q, want unset (would reload and wipe the rest of the kept list)", rec.Header().Get("X-UT-Response"))
	}
}

// ut-docs#1858: RemoveDemoPromo's own endpoint, mirroring
// TestSettingsRemoveDemoItemEndpoint exactly for the promo side.
func TestSettingsRemoveDemoPromoEndpoint(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCustomersPromos(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.Db.Exec(`UPDATE promotions SET is_active = 0 WHERE code = 'PROMO500'`); err != nil {
		t.Fatal(err)
	}

	// Cashier: forbidden, promo still there.
	postForm(mux, "/api/settings/demo-promo/PROMO500/remove", url.Values{}, &cashUser)
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code = 'PROMO500'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("cashier's request removed PROMO500")
	}

	// Manager: removed, and the response confirms it.
	rec := postForm(mux, "/api/settings/demo-promo/PROMO500/remove", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Removed") {
		t.Fatalf("manager remove-anyway: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code = 'PROMO500'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("PROMO500 survived the manager's remove-anyway request")
	}
}

// The server-side re-check refuses a promo that is actually targeted at a
// customer, regardless of what the client believed when it rendered the
// button — mirrors TestSettingsRemoveDemoItemEndpoint_RefusesItemWithHistory.
func TestSettingsRemoveDemoPromoEndpoint_RefusesTargetedPromo(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCustomersPromos(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.Db.Exec(`UPDATE promotions SET customer_id = 'cust-001' WHERE code = 'PROMO50'`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/settings/demo-promo/PROMO50/remove", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("remove-anyway on a targeted promo: code=%d body=%s, want a refusal", rec.Code, rec.Body.String())
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM promotions WHERE code = 'PROMO50'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("PROMO50 was removed despite being targeted at a customer")
	}
}

// ut-docs#1858's other resolution: "keep as my own" — mirrors
// TestSettingsKeepDemoItemEndpoint.
func TestSettingsKeepDemoPromoEndpoint(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCustomersPromos(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.Db.Exec(`UPDATE promotions SET is_active = 0 WHERE code = 'PROMO500'`); err != nil {
		t.Fatal(err)
	}

	// Cashier: forbidden, still flagged sample.
	postForm(mux, "/api/settings/demo-promo/PROMO500/keep", url.Values{}, &cashUser)
	var flagged int
	if err := d.Db.QueryRow(`SELECT is_sample_data FROM promotions WHERE code = 'PROMO500'`).Scan(&flagged); err != nil {
		t.Fatal(err)
	}
	if flagged != 1 {
		t.Fatal("cashier's request cleared is_sample_data")
	}

	rec := postForm(mux, "/api/settings/demo-promo/PROMO500/keep", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Kept") {
		t.Fatalf("manager keep-as-own: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := d.Db.QueryRow(`SELECT is_sample_data FROM promotions WHERE code = 'PROMO500'`).Scan(&flagged); err != nil {
		t.Fatal(err)
	}
	if flagged != 0 {
		t.Fatal("PROMO500 still flagged is_sample_data after keep-as-own")
	}
}

// ut-docs#1858, mirroring TestSettingsRemoveDemoCatalogueEndpoint_EditedItemRowHasBothButtons:
// the KeptReasonEdited branch of the promo kept list — its two buttons,
// their hx-post URLs, and the shared message span id — must actually be
// present, not just claimed by a code comment.
func TestSettingsRemoveDemoCatalogueEndpoint_EditedPromoRowHasBothButtons(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCustomersPromos(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Real trading history elsewhere forces strict mode, so the edited promo
	// is KEPT (not removed outright) and shows up in the rendered list.
	if _, err := d.Db.Exec(`INSERT INTO items (id, name, base_price) VALUES ('own-1', 'My Own Item', 250)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s-1', 'R-1', 250, 250)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sale_lines
		(id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		VALUES ('sl-1', 's-1', 1, 'own-1', 'My Own Item', 1, 250, 0, 0, 250, 250)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`UPDATE promotions SET is_active = 0 WHERE code = 'PROMO500'`); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager remove: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`hx-post="/api/settings/demo-promo/PROMO500/remove"`,
		`hx-post="/api/settings/demo-promo/PROMO500/keep"`,
		`hx-target="[id=&#34;demo-promo-msg-PROMO500&#34;]"`,
		`id="demo-promo-msg-PROMO500"`,
		"PROMO500",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("edited-promo row missing %q\nbody=%s", want, body)
		}
	}
}

// ut-docs#2191: newRealDBDeps now clones a once-built, fully-migrated
// template file per call instead of re-running the whole migration chain 48
// times per test binary. That only stays correct if every caller still gets
// its OWN independent copy: a write through one handle must never be visible
// through the next, and the clone must be a real migrated database (not just
// a file that happens to open). The pointer-identity check pins the "built
// once per process" half — a second realDBTemplate call must hand back the
// same cached slice, never a rebuilt one.
func TestNewRealDBDeps_TemplateClonesAreIsolatedAndFullyMigrated(t *testing.T) {
	first := realDBTemplate(t)
	second := realDBTemplate(t)
	if len(first) == 0 {
		t.Fatal("realDBTemplate returned an empty template")
	}
	if &first[0] != &second[0] {
		t.Fatal("realDBTemplate rebuilt the template on a second call; want the sync.Once-cached bytes")
	}

	_, a := newRealDBDeps(t)
	if err := a.Settings.Set(t.Context(), common.KeyShopType, "cafe"); err != nil {
		t.Fatalf("Set on first deps: %v", err)
	}
	if v, ok, _ := a.Settings.Get(t.Context(), common.KeyShopType); !ok || v != "cafe" {
		t.Fatalf("first deps did not see its own write: %q ok=%v", v, ok)
	}

	_, b := newRealDBDeps(t)
	if v, ok, err := b.Settings.Get(t.Context(), common.KeyShopType); err != nil || ok {
		t.Fatalf("second deps saw the first deps' write: %q ok=%v err=%v — template clone is not isolated", v, ok, err)
	}
	// A migrated schema, not just a file that opens: the demo-seed repo's
	// count needs the full catalogue tables 001_init.sql creates.
	n, err := data.NewDemoSeedRepo(b.Db).SampleItemCount(t.Context())
	if err != nil {
		t.Fatalf("SampleItemCount on cloned db: %v", err)
	}
	if n != 0 {
		t.Fatalf("cloned db already has %d sample items; template must be pristine", n)
	}
}
