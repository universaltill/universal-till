package pages

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	// ut-docs#1750: Cfg + State are needed now that the mutating endpoints
	// gate on d.CurrentState().Country. Deps.State is a VALUE, so
	// CurrentState() on a zero Deps returns Country == "" and the gate
	// fails closed rather than panicking (review F5 corrected an earlier
	// comment here); what needs Cfg is common.LoadState, which nil-derefs
	// without it.
	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "TRY", TaxRate: 20}}
	d := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		AuthSvc:  auth.NewService(db),
		Settings: settings.NewStore(db),
		State:    common.LoadState(context.Background(), settings.NewStore(db), cfg),
	}
	mux := http.NewServeMux()
	registerFiscalDeviceTR(mux, d)
	return mux, d
}

func en(key string) string { return httpx.T("en", key) }

func TestFiscalDevicePage_RendersWithoutPlugin(t *testing.T) {
	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	// ut-docs#1750: the actions are gated on the market, so this test — which
	// asserts the confirm form renders — needs a shop that flow is FOR. The
	// plugin-missing notice it also asserts is a separate, unrelated branch.
	seedActiveTaxTrPlugin(t, d.Db, false) // installed but INACTIVE: still "missing" to the page
	setCountry(t, d, "TR")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fiscal-device", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{en("fiscaldevice.plugin.missing"), en("fiscaldevice.state.not_confirmed"), en("fiscaldevice.last.none")} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in page, got: %s", want, body)
		}
	}
	// ut-docs#1750: with no ACTIVE plugin the confirm action is not offered —
	// it would POST to an endpoint that refuses, and the manual's own
	// screenshot used to show that dead button. The page still explains
	// itself, which is the whole reason the GET stays reachable.
	if strings.Contains(body, `action="/api/fiscal-device/confirm"`) {
		t.Fatalf("confirm form must not render without an active plugin, got: %s", body)
	}
	if !strings.Contains(body, en("fiscaldevice.actions.unavailable")) {
		t.Fatalf("expected the actions-unavailable explanation, got: %s", body)
	}
}

// ut-docs#2116: GET /fiscal-device now renders inside the /admin two-pane
// shell, with the /fiscal-device tree row marked selected -- but ONLY once
// the tree itself actually includes that entry (TR + the plugin active,
// same fiscalDeviceMarketActive-shaped gate visibleAdminEntries applies).
// This is the wrinkle worth its own dedicated test: the GET page itself
// stays reachable on ANY country/plugin state (fiscal_device_page.go's own
// doc comment -- the docs-shots harness renders it with nothing
// installed), but the shell must never show it as the CURRENT tree row
// when the viewer's own tree wouldn't otherwise list it (AC #6) -- proven
// separately below.
func TestFiscalDevicePage_BareGETRendersInsideAdminShellWithSelectionMarkedOnceVisible(t *testing.T) {
	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxTrPlugin(t, d.Db, true)
	setCountry(t, d, "TR")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fiscal-device", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /fiscal-device = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="admin-tree"`) || !strings.Contains(body, `id="admin-panel"`) {
		t.Fatalf("expected the admin shell rendered, got: %s", body)
	}
	if !strings.Contains(body, `class="items-row is-current"`) {
		t.Fatalf("expected the /fiscal-device tree row marked is-current, got: %s", body)
	}
	if !strings.Contains(body, `aria-current="page"`) {
		t.Fatalf("expected aria-current=\"page\" on the selected row, got: %s", body)
	}
	// The page's own plugin-settings/plugins-store links stay plain
	// navigations OUT of the admin area, untouched by the shell wrap.
	if !strings.Contains(body, `href="/plugins`) {
		t.Fatalf("expected the plugin-settings link still present and untouched, got: %s", body)
	}
}

// AC #6 counterpart to the test above: on a shop where the tree itself
// would never list /fiscal-device (no TR + active plugin), the bare GET
// still succeeds (the page's own gate is unchanged) but the shell must not
// show a phantom selection -- there is no tree row for this route at all
// to mark current.
func TestFiscalDevicePage_BareGETShowsNoTreeEntryWhenMarketConditionUnmet(t *testing.T) {
	mux, _ := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fiscal-device", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /fiscal-device = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `href="/fiscal-device"`) {
		t.Fatalf("expected no /fiscal-device tree entry when the market condition is unmet, got: %s", body)
	}
}

// ut-docs#2116: an htmx panel-swap request gets only the destination's own
// content plus an out-of-band admin-tree refresh, mirroring
// TestLocationsPage_FragmentSwapReturnsContentPlusOOBTreeWithSelectionMarked.
func TestFiscalDevicePage_FragmentSwapReturnsContentPlusOOBTreeWithSelectionMarked(t *testing.T) {
	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxTrPlugin(t, d.Db, true)
	setCountry(t, d, "TR")

	req := httptest.NewRequest(http.MethodGet, "/fiscal-device", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fragment GET /fiscal-device = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `class="nav"`) {
		t.Fatalf("expected a content-only fragment with no nav chrome, got: %s", body)
	}
	if !strings.Contains(body, `hx-swap-oob="true"`) || !strings.Contains(body, `id="admin-tree"`) {
		t.Fatalf("expected an out-of-band admin-tree refresh appended, got: %s", body)
	}
	if !strings.Contains(body, `class="items-row is-current"`) {
		t.Fatalf("expected the /fiscal-device row marked is-current in the OOB tree, got: %s", body)
	}
}

func TestFiscalDevicePage_ShowsPluginSettingsAndLastReceipt(t *testing.T) {
	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxTrPlugin(t, d.Db, true)
	setCountry(t, d, "TR")
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
	_ = d.Settings.Set(t.Context(), fiscal.SigningDeviceConfiguredKey("TR"), "true")
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
	// ut-docs#1750: these endpoints are now gated on the shop actually being
	// the market this flow is for — Türkiye with the tax-tr plugin active.
	seedActiveTaxTrPlugin(t, d.Db, true)
	setCountry(t, d, "TR")
	repo := data.NewPOSRepo(d.Db)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/fiscal-device/confirm", nil))
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "fiscaldevice.msg.confirmed") {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("TR")); v != "true" {
		t.Fatalf("TR's signing_device_configured after confirm = %q, want true", v)
	}
	// ADR-0083: the Turkish page writes Turkey's row and nothing else.
	if v, ok, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); ok && v != "" {
		t.Fatalf("confirm must not touch Germany's row, got %q", v)
	}
	if ok, _ := repo.HasAuditEntry(t.Context(), "fiscal_device", "till", fiscalDeviceAuditConfirmed); !ok {
		t.Fatal("expected a fiscal_device_confirmed audit row")
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/fiscal-device/unpair", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("unpair: %d", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("TR")); v != "false" {
		t.Fatalf("TR's signing_device_configured after unpair = %q, want false", v)
	}
	if ok, _ := repo.HasAuditEntry(t.Context(), "fiscal_device", "till", fiscalDeviceAuditUnpaired); !ok {
		t.Fatal("expected a fiscal_device_unpaired audit row")
	}
}

// ut-docs#2008: fiscal-device (Group: "menu.group.administration", like
// fiscal-register) no longer gets its own /menu tile in any state -- even
// TR + the Turkish fiscal-device plugin active, which used to be exactly
// the state that unlocked this tile directly. The full TR + plugin-state
// visibility matrix this test used to check moved to admin_page_test.go
// (GET /admin's Fiscal cluster, TestAdminPage_FiscalClusterRequiresTurkey-
// AndPlugin) -- this is the regression check that /menu itself stays empty
// of it, in every state including the one that used to unlock it.
func TestMenuPage_FiscalDeviceTileNeverRendersDirectlyOnMenu(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, nil)
	t.Setenv("UT_AUTH", "off")
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "TR" })
	seedActiveTaxTrPlugin(t, dp.Db, true)

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `href="/fiscal-device"`) {
		t.Fatalf("expected no direct /fiscal-device tile on /menu even with TR + plugin active (ut-docs#2008: it now lives inside /admin), got: %s", body)
	}
	if !strings.Contains(body, `href="/admin"`) {
		t.Fatalf("expected the /admin tile once at least one admin destination is visible, got: %s", body)
	}
}

// setCountry sets the shop's country and reloads the cached state the
// handlers read through d.CurrentState().
func setCountry(t *testing.T, d *common.Deps, code string) {
	t.Helper()
	if err := settings.NewStore(d.Db).Set(t.Context(), "store.country", code); err != nil {
		t.Fatalf("set store.country: %v", err)
	}
	d.State = common.LoadState(t.Context(), settings.NewStore(d.Db), d.Cfg)
}

// ut-docs#1750, independent review finding 2. This is the severe one: between
// ADR-0081 and ADR-0083 the Turkish device page wrote the SAME settings key
// that Germany's TSE gate read, and registerFiscalDeviceTR is registered on
// every till regardless of country. (ADR-0083 split the rows per country,
// so the page now cannot reach Germany's row at all; the market guard stays
// as the thing that keeps a non-Turkish shop from declaring an ÖKC posture.)
// So one manager POST to /api/fiscal-device/confirm on a GERMAN till lifted
// BlockedNeverConfigured — the one state ADR-0048 Decision 2.2 says has no
// override path — and every sale after it completed unsigned.
//
// The German route to that same flag costs a real TSE credential written to
// the store and read back off disk (setup_tse.go). A button must not be a
// shortcut around it.
func TestFiscalDeviceConfirm_RefusedOutsideTurkey(t *testing.T) {
	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxTrPlugin(t, d.Db, true) // even with the plugin active
	setCountry(t, d, "DE")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/fiscal-device/confirm", nil))
	if rec.Code == http.StatusSeeOther || rec.Code == http.StatusOK {
		t.Fatalf("confirm must be refused on a DE till, got %d", rec.Code)
	}
	for _, cc := range []string{"DE", "TR"} {
		if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey(cc)); v == "true" {
			t.Fatalf("a DE till's %s posture row was flipped by the Turkish device page — ADR-0048 Decision 2.2 says this state has no override path", cc)
		}
	}
}

// The mirror: unpair sets the same key false, so on a German till it is a
// one-click way to hard-block a shop's checkout with no route back except
// this same page.
func TestFiscalDeviceUnpair_RefusedOutsideTurkey(t *testing.T) {
	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxTrPlugin(t, d.Db, true)
	setCountry(t, d, "DE")
	if err := d.Settings.Set(t.Context(), fiscal.SigningDeviceConfiguredKey("DE"), "true"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/fiscal-device/unpair", nil))
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); v != "true" {
		t.Fatalf("a DE till's configured flag was cleared by the Turkish device page (now %q) — that hard-blocks checkout", v)
	}
}

// A TR shop running the device plugin must still be able to confirm — the
// gate narrows who can reach this, it does not remove the feature.
func TestFiscalDeviceConfirm_AllowedForTurkeyWithPlugin(t *testing.T) {
	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxTrPlugin(t, d.Db, true)
	setCountry(t, d, "TR")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/fiscal-device/confirm", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 for a TR till with the plugin active, got %d: %s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("TR")); v != "true" {
		t.Fatalf("TR confirm must set the flag, got %q", v)
	}
}

// A TR shop with no device plugin is in shadow mode beside its existing
// register — it is not driving a device, so it must not be able to declare
// one confirmed either.
func TestFiscalDeviceConfirm_RefusedForTurkeyWithoutPlugin(t *testing.T) {
	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	setCountry(t, d, "TR") // no plugin seeded

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/fiscal-device/confirm", nil))
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("TR")); v == "true" {
		t.Fatal("confirm must be refused for a TR till with no fiscal-device plugin active")
	}
}

// ut-docs#1750 (review F1, defence in depth). Confirm writes the same fiscal
// posture flag that /api/settings/upsert guards behind the owner-only
// "fiscal_tse_override" permission (see fiscal_gate_test.go's "manager cannot
// flip fiscal toggles"). A button must not be a weaker path to the same state
// than the settings editor is — so this runs with auth ON, unlike the tests
// above, or canPerform short-circuits to true and proves nothing.
func TestFiscalDeviceConfirm_RequiresOwnerPermissionNotJustManager(t *testing.T) {
	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "on")
	seedActiveTaxTrPlugin(t, d.Db, true)
	setCountry(t, d, "TR")
	for _, u := range []struct {
		id, role string
	}{{"mgr9", "manager"}, {"own1", "admin"}} {
		if _, err := d.Db.Exec(`INSERT INTO users(id,username,display_name,pin_hash,role) VALUES(?,?,?,'x',?)`, u.id, u.id, u.id, u.role); err != nil {
			t.Fatal(err)
		}
	}

	post := func(u auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/fiscal-device/confirm", nil)
		req = auth.WithUser(req, u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := post(auth.User{ID: "mgr9", Role: "manager"}); rec.Code != http.StatusForbidden {
		t.Fatalf("a manager must not be able to declare fiscal posture by hand, got %d", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("TR")); v == "true" {
		t.Fatal("manager confirm must not have set the flag")
	}
	if rec := post(auth.User{ID: "own1", Role: "admin"}); rec.Code != http.StatusSeeOther {
		t.Fatalf("an owner must still be able to confirm, got %d: %s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("TR")); v != "true" {
		t.Fatalf("owner confirm must set the flag, got %q", v)
	}
}

// ut-docs#1750 (third review, F6). Hiding the actions behind .marketActive
// left every assertion on the confirm form NEGATIVE, so a change that hid
// pairing from every legitimate Turkish shop would have passed the suite.
// This is the positive control.
func TestFiscalDevicePage_ShowsConfirmForTurkeyWithActivePluginAndNoDevice(t *testing.T) {
	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxTrPlugin(t, d.Db, true)
	setCountry(t, d, "TR")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fiscal-device", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `action="/api/fiscal-device/confirm"`) {
		t.Fatalf("a TR shop with an active plugin and no confirmed device must be offered pairing, got: %s", body)
	}
	if strings.Contains(body, en("fiscaldevice.actions.unavailable")) {
		t.Fatalf("the unavailable notice must not show when actions ARE available, got: %s", body)
	}
}

// ut-docs#1786 (last slice of #1458): every error this page can answer with
// must render the full operator layout (nav rail, "Back to sale"), not a
// bare untranslated body — on a pinned Android kiosk a bare body is a dead
// end with no way back. #1663 already proved this pattern for the sibling
// German page; this is the same proof for the 6 sites #1663 deliberately
// deferred here to avoid colliding with the then-in-flight Turkey ÖKC work.
func TestFiscalDevicePage_EveryErrorRendersFullLayout(t *testing.T) {
	assertFullLayoutError := func(t *testing.T, rec *httptest.ResponseRecorder, wantCode int) {
		t.Helper()
		if rec.Code != wantCode {
			t.Fatalf("code = %d, want %d: %s", rec.Code, wantCode, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `class="nav"`) {
			t.Fatalf("error response has no nav rail (bare body, dead end on a pinned kiosk):\n%s", body)
		}
	}

	// Site 1 (line 88): the shared requireManager gate every handler below
	// goes through first — a non-manager on any of them.
	t.Run("non-manager GET", func(t *testing.T) {
		mux, d := newFiscalDeviceTestMux(t)
		seedActiveTaxTrPlugin(t, d.Db, true)
		setCountry(t, d, "TR")
		cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/fiscal-device", nil), cashier)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assertFullLayoutError(t, rec, http.StatusForbidden)
	})

	// Site 2 (line 116, inside render): posRepo.LatestFiscalDeviceReceipt
	// fails — force it by closing the DB out from under an otherwise-valid
	// manager GET.
	t.Run("GET with a failing repo read", func(t *testing.T) {
		mux, d := newFiscalDeviceTestMux(t)
		t.Setenv("UT_AUTH", "off")
		seedActiveTaxTrPlugin(t, d.Db, true)
		setCountry(t, d, "TR")
		if err := d.Db.Close(); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fiscal-device", nil))
		assertFullLayoutError(t, rec, http.StatusInternalServerError)
	})

	// Sites 3 & 5 (lines 195, 235): confirm/unpair when d.Settings is nil —
	// the config-not-loaded case neither handler can proceed past.
	for _, tt := range []struct {
		name string
		path string
	}{
		{"confirm with no Settings store", "/api/fiscal-device/confirm"},
		{"unpair with no Settings store", "/api/fiscal-device/unpair"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux, d := newFiscalDeviceTestMux(t)
			t.Setenv("UT_AUTH", "off")
			seedActiveTaxTrPlugin(t, d.Db, true)
			setCountry(t, d, "TR")
			d.Settings = nil
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tt.path, nil))
			assertFullLayoutError(t, rec, http.StatusInternalServerError)
		})
	}

	// Sites 4 & 6 (lines 199, 239): confirm/unpair when d.Settings.Set
	// itself fails — drop the settings table so Set fails while everything
	// the market-active gate needs (the plugins tables) stays intact, so
	// the request actually reaches the Set call instead of 404ing earlier.
	for _, tt := range []struct {
		name string
		path string
	}{
		{"confirm with a failing Settings.Set", "/api/fiscal-device/confirm"},
		{"unpair with a failing Settings.Set", "/api/fiscal-device/unpair"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux, d := newFiscalDeviceTestMux(t)
			t.Setenv("UT_AUTH", "off")
			seedActiveTaxTrPlugin(t, d.Db, true)
			setCountry(t, d, "TR")
			if _, err := d.Db.Exec(`DROP TABLE settings`); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tt.path, nil))
			assertFullLayoutError(t, rec, http.StatusInternalServerError)
		})
	}

	// Sites 7 & 9 (ut-docs#1814): confirm/unpair's "owner (admin) required"
	// gate (canPerform("fiscal_tse_override") — admin/super_admin only, see
	// TestFiscalDeviceConfirm_RequiresOwnerPermissionNotJustManager for the
	// status-code proof) used to answer with a bare, untranslated
	// http.Error body — this is the layout/i18n proof for the same gate.
	for _, tt := range []struct {
		name string
		path string
	}{
		{"confirm refused for a manager (owner-only gate)", "/api/fiscal-device/confirm"},
		{"unpair refused for a manager (owner-only gate)", "/api/fiscal-device/unpair"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux, d := newFiscalDeviceTestMux(t)
			t.Setenv("UT_AUTH", "on")
			seedActiveTaxTrPlugin(t, d.Db, true)
			setCountry(t, d, "TR")
			if _, err := d.Db.Exec(`INSERT INTO users(id,username,display_name,pin_hash,role) VALUES('mgr-layout','mgr-layout','mgr-layout','x','manager')`); err != nil {
				t.Fatal(err)
			}
			req := auth.WithUser(httptest.NewRequest(http.MethodPost, tt.path, nil), auth.User{ID: "mgr-layout", Role: "manager"})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			assertFullLayoutError(t, rec, http.StatusForbidden)
			if !strings.Contains(rec.Body.String(), en("fiscaldevice.error.owner_required")) {
				t.Fatalf("expected the translated owner_required message, got: %s", rec.Body.String())
			}
		})
	}

	// Sites 8 & 10 (ut-docs#1814): confirm/unpair's "not found" gate
	// (fiscalDeviceMarketActive — a non-TR till, or TR without the active
	// plugin) also used to answer with a bare, untranslated http.Error
	// body. An admin passes the owner-only gate above so the request
	// actually reaches this second check instead of 403ing first.
	for _, tt := range []struct {
		name string
		path string
	}{
		{"confirm not found outside Turkey", "/api/fiscal-device/confirm"},
		{"unpair not found outside Turkey", "/api/fiscal-device/unpair"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux, d := newFiscalDeviceTestMux(t)
			t.Setenv("UT_AUTH", "on")
			setCountry(t, d, "DE") // not Turkey — fiscalDeviceMarketActive is false regardless of any plugin
			if _, err := d.Db.Exec(`INSERT INTO users(id,username,display_name,pin_hash,role) VALUES('own-layout','own-layout','own-layout','x','admin')`); err != nil {
				t.Fatal(err)
			}
			req := auth.WithUser(httptest.NewRequest(http.MethodPost, tt.path, nil), auth.User{ID: "own-layout", Role: "admin"})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			assertFullLayoutError(t, rec, http.StatusNotFound)
			if !strings.Contains(rec.Body.String(), en("fiscaldevice.error.not_found")) {
				t.Fatalf("expected the translated not_found message, got: %s", rec.Body.String())
			}
		})
	}
}

// ut-docs#1938: the last-receipt IssuedAt/CreatedAt cell used to render the
// raw RFC3339 string unwrapped, instead of going through the locale-aware
// `datetime` template func (ut-docs#1130/#1632), same gap #1894 fixed at
// six other sites but missed here. time.Local pinned to UTC for
// determinism, same convention as TestAuditPage_RendersLocaleFormattedCreatedAt.
func TestFiscalDevicePage_RendersLocaleFormattedIssuedAt(t *testing.T) {
	orig := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = orig })

	mux, d := newFiscalDeviceTestMux(t)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxTrPlugin(t, d.Db, true)
	setCountry(t, d, "TR")
	repo := data.NewPOSRepo(d.Db)
	const rawIssuedAt = "2026-09-03T10:00:00+03:00"
	if err := repo.RecordFiscalDeviceReceipt(t.Context(), data.FiscalDeviceReceipt{SaleID: "s1", Maker: "beko", Serial: "AV777", ReceiptNo: "0000042", ZNo: 9, IssuedAt: rawIssuedAt}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fiscal-device?lang=de-DE", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, rawIssuedAt) {
		t.Fatalf("last-receipt row must not show the raw RFC3339 timestamp: %s", body)
	}
	// 2026-09-03T10:00:00+03:00 in UTC (time.Local above) is 07:00.
	if !strings.Contains(body, "03.09.2026 07:00") {
		t.Fatalf("last-receipt row must show the de-DE-formatted date+time: %s", body)
	}
}
