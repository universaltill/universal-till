package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// seedActiveTaxTrPlugin mirrors seedActiveTaxDePlugin for the Turkish
// fiscal-device plugin (fiscal.PluginIDTaxTR) on this file's plain
// `plugins` fixture table.
func seedActiveTaxTrPlugin(t *testing.T, db *sql.DB, active bool) {
	t.Helper()
	is := 0
	if active {
		is = 1
	}
	// openPagesTestDB runs the real migrated schema (ut-docs#1657/#1676),
	// where plugins.entrypoint is NOT NULL and plugins carries a composite
	// FOREIGN KEY (id, version) REFERENCES plugin_catalog (id, version) — so
	// the catalog row has to exist first. Exactly the shape the German
	// equivalent already uses (seedTaxDeCatalogRow, fiscal_register_page_test.go).
	if _, err := db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at)
VALUES (?, '0.1.0', 'Türkiye fiscal device', 'desc', 'go', 'entry', 'url', 'sha', 'auth', 'site', '[]', '0.0.0', '1', datetime('now'))`, fiscal.PluginIDTaxTR); err != nil {
		t.Fatalf("seed tax-tr plugin_catalog: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, is_active) VALUES (?, 'Türkiye fiscal device', '0.1.0', 'entry', ?)`, fiscal.PluginIDTaxTR, is); err != nil {
		t.Fatalf("seed tax-tr plugin: %v", err)
	}
}

func newFiscalDeviceTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	// Real English strings, so assertions read what an operator reads
	// (same init newMenuPageTestDeps does); en() below resolves a key.
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	d := &common.Deps{Db: db, Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: auth.NewService(db), Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerFiscalDeviceTR(mux, d)
	return mux, d
}

func en(key string) string { return httpx.T("en", key) }

func TestFiscalDevicePage_RendersWithoutPlugin(t *testing.T) {
	mux, _ := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fiscal-device", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{en("fiscaldevice.plugin.missing"), en("fiscaldevice.state.not_confirmed"), en("fiscaldevice.last.none"), `action="/api/fiscal-device/confirm"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in page, got: %s", want, body)
		}
	}
}

func TestFiscalDevicePage_ShowsPluginSettingsAndLastReceipt(t *testing.T) {
	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxTrPlugin(t, d.Db, true)
	prepo := data.NewPluginRepo(d.Db)
	for k, v := range map[string]string{"okc.driver": `"bridge"`, "okc.host": `"192.168.1.50"`, "okc.port": `"4711"`, "okc.maker": `"beko"`} {
		if err := prepo.UpsertPluginSettingScoped(t.Context(), fiscal.PluginIDTaxTR, k, v, "global", false); err != nil {
			t.Fatalf("seed setting %s: %v", k, err)
		}
	}
	repo := data.NewPOSRepo(d.Db)
	if err := repo.RecordFiscalDeviceReceipt(t.Context(), data.FiscalDeviceReceipt{SaleID: "s1", Maker: "beko", Serial: "AV777", ReceiptNo: "0000042", ZNo: 9, IssuedAt: "2026-09-03T10:00:00+03:00"}); err != nil {
		t.Fatal(err)
	}
	_ = d.Settings.Set(t.Context(), fiscal.KeySigningDeviceConfigured, "true")
	_ = d.Settings.Set(t.Context(), fiscal.KeySystemOfRecord, "true")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fiscal-device", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{en("fiscaldevice.plugin.active"), "192.168.1.50", "4711", "beko", "0000042", "AV777", en("fiscaldevice.state.confirmed"), en("fiscaldevice.state.system_of_record"), `action="/api/fiscal-device/unpair"`, "/plugins/" + fiscal.PluginIDTaxTR + "/settings"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in page, got: %s", want, body)
		}
	}
	if strings.Contains(body, `action="/api/fiscal-device/confirm"`) {
		t.Fatal("confirm button must not show once the device is confirmed")
	}
}

func TestFiscalDevicePage_ConfirmAndUnpairFlipTheGateFlagWithAudit(t *testing.T) {
	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	repo := data.NewPOSRepo(d.Db)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/fiscal-device/confirm", nil))
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "fiscaldevice.msg.confirmed") {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.KeySigningDeviceConfigured); v != "true" {
		t.Fatalf("tse_configured after confirm = %q, want true", v)
	}
	if ok, _ := repo.HasAuditEntry(t.Context(), "fiscal_device", "till", fiscalDeviceAuditConfirmed); !ok {
		t.Fatal("expected a fiscal_device_confirmed audit row")
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/fiscal-device/unpair", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("unpair: %d", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.KeySigningDeviceConfigured); v != "false" {
		t.Fatalf("tse_configured after unpair = %q, want false", v)
	}
	if ok, _ := repo.HasAuditEntry(t.Context(), "fiscal_device", "till", fiscalDeviceAuditUnpaired); !ok {
		t.Fatal("expected a fiscal_device_unpaired audit row")
	}
}

// The Turkish fiscal-device tile follows the German one's rule: country
// AND plugin installed+active, never country alone.
func TestMenuPage_FiscalDeviceTileRequiresTurkeyAndPlugin(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, nil)
	t.Setenv("UT_AUTH", "off")
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "TR" })

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `href="/fiscal-device"`) {
		t.Fatalf("expected no fiscal-device tile for TR with no plugin, got: %s", rec.Body.String())
	}

	seedActiveTaxTrPlugin(t, dp.Db, true)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `href="/fiscal-device"`) || !strings.Contains(body, "🧾") {
		t.Fatalf("expected the fiscal-device tile once TR + plugin active, got: %s", body)
	}

	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `href="/fiscal-device"`) {
		t.Fatal("fiscal-device tile must be Turkey-only")
	}
}
