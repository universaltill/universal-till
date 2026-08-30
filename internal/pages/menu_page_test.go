package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func newMenuPageTestDeps(t *testing.T, menu []common.MenuItem) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     menu,
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerMenu(mux, dp)
	return mux, dp
}

func TestMenuPage_RendersConfiguredTilesWithMappedIcons(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, []common.MenuItem{
		{Href: "/inventory", Label: "nav.inventory"},
		{Href: "/reports", Label: "nav.reports"},
	})
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/inventory"`) || !strings.Contains(body, "📦") {
		t.Fatalf("expected the inventory tile with its mapped icon, got: %s", body)
	}
	if !strings.Contains(body, `href="/reports"`) || !strings.Contains(body, "📊") {
		t.Fatalf("expected the reports tile with its mapped icon, got: %s", body)
	}
	// Tile order follows d.Menu order -- it's the actual on-screen layout,
	// so a reordering regression should fail this.
	if i, r := strings.Index(body, `href="/inventory"`), strings.Index(body, `href="/reports"`); i > r {
		t.Fatalf("expected /inventory tile before /reports tile (d.Menu order), got: %s", body)
	}
	if !strings.Contains(body, "/menu?lang=") {
		t.Fatalf("expected the language-switcher links, got: %s", body)
	}
}

func TestMenuPage_UnmappedRouteGetsFallbackIcon(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, []common.MenuItem{
		{Href: "/some-plugin-page", Label: "Custom Plugin"},
	})
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/some-plugin-page"`) {
		t.Fatalf("expected the plugin tile rendered, got: %s", body)
	}
	if !strings.Contains(body, "▪️") {
		t.Fatalf("expected the fallback icon for an unmapped route, got: %s", body)
	}
}

func TestMenuPage_AlwaysAddsHelpTile(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `href="/help"`) {
		t.Fatalf("expected the /help tile even with an empty configured menu, got: %s", rec.Body.String())
	}
}

func TestMenuPage_ManagerOnlyTilesGatedByRole(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, nil)

	// UT_AUTH is unset (not "off") and no session user is attached to the
	// request, so isManagerOrAuthOff must be false here.
	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `href="/users"`) {
		t.Fatalf("expected no /users tile for a non-manager request, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `href="/report-issue"`) {
		t.Fatalf("expected no /report-issue tile for a non-manager request, got: %s", rec.Body.String())
	}

	t.Setenv("UT_AUTH", "off")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	body := rec2.Body.String()
	if !strings.Contains(body, `href="/users"`) || !strings.Contains(body, `href="/translations"`) {
		t.Fatalf("expected the manager-only tiles with UT_AUTH=off, got: %s", body)
	}
	if !strings.Contains(body, `href="/report-issue"`) || !strings.Contains(body, "🐞") {
		t.Fatalf("expected the report-issue tile with its icon, reachable from the menu with UT_AUTH=off, got: %s", body)
	}
	if !strings.Contains(body, `href="/locations"`) || !strings.Contains(body, "📍") {
		t.Fatalf("expected the locations tile with its icon, reachable from the menu with UT_AUTH=off, got: %s", body)
	}
	if strings.Contains(rec.Body.String(), `href="/locations"`) {
		t.Fatalf("expected no /locations tile for a non-manager request, got: %s", rec.Body.String())
	}
}

// ut-docs#1084: the fiscal-register tile requires BOTH country=DE and the
// German tax plugin installed+active -- country alone (ut-docs#1026's
// objection) must no longer be sufficient.
func TestMenuPage_FiscalRegisterTileRequiresPluginNotJustCountry(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, nil)
	t.Setenv("UT_AUTH", "off")
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `href="/fiscal-register"`) {
		t.Fatalf("expected no fiscal-register tile for DE with no plugin installed, got: %s", rec.Body.String())
	}

	seedActiveTaxDePlugin(t, dp.Db)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	body := rec2.Body.String()
	if !strings.Contains(body, `href="/fiscal-register"`) || !strings.Contains(body, "📋") {
		t.Fatalf("expected the fiscal-register tile once DE + plugin active, got: %s", body)
	}
}

// The gate checks is_active, not merely row existence -- a plugin that's
// installed but disabled (ut-docs#531's precedent: a merchant who imports
// before enabling it) must be treated the same as not installed at all.
func TestMenuPage_FiscalRegisterTileHiddenWhenPluginDisabled(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, nil)
	t.Setenv("UT_AUTH", "off")
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	seedDisabledTaxDePlugin(t, dp.Db)

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `href="/fiscal-register"`) {
		t.Fatalf("expected no fiscal-register tile for DE with the plugin installed but disabled, got: %s", rec.Body.String())
	}
}

// A non-DE shop must never see the tile even with the plugin installed --
// country stays a necessary pre-filter, it just isn't sufficient alone
// any more.
func TestMenuPage_FiscalRegisterTileHiddenOutsideGermanyEvenWithPlugin(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, nil)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxDePlugin(t, dp.Db)

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `href="/fiscal-register"`) {
		t.Fatalf("expected no fiscal-register tile outside Germany even with the plugin active, got: %s", rec.Body.String())
	}
}

// ut-docs#1208 widened fiscal.RequiresHardGate to also cover Turkey (an
// unrelated YN ÖKC obligation, not §146a Abs. 4 AO) -- this tile's gate must
// stay an explicit country=="DE" check, not fiscal.RequiresHardGate, or a TR
// shop would wrongly start seeing Germany's fiscal-register tile the moment
// it happened to have the (unrelated) German tax plugin active.
func TestMenuPage_FiscalRegisterTileHiddenForTurkeyEvenWithGermanPluginActive(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, nil)
	t.Setenv("UT_AUTH", "off")
	seedActiveTaxDePlugin(t, dp.Db)
	if err := settings.NewStore(dp.Db).Set(t.Context(), "store.country", "TR"); err != nil {
		t.Fatalf("set store.country: %v", err)
	}
	dp.State = common.LoadState(t.Context(), settings.NewStore(dp.Db), dp.Cfg)

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `href="/fiscal-register"`) {
		t.Fatalf("expected no fiscal-register tile for TR even with the German plugin active, got: %s", rec.Body.String())
	}
}

// ut-docs#1125: the menu page's language row is the third render site for
// httpx.AvailableLocales() (after the setup wizard's language step and
// settings' default-locale picker) and must show each locale's native name,
// never the bare two-letter code — a non-technical operator doesn't know "fa"
// is فارسی. Pinned by a test rather than left to the manual's screenshots:
// `make docs-shots` captures /menu above the fold, so this row (which sits
// below the tile grid) never appears in menu.png and a regression here would
// be invisible to both CI and the manual.
func TestMenuPageLanguageRowShowsNativeNamesNotBareCodes(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, []common.MenuItem{{Href: "/inventory", Label: "nav.inventory"}})
	t.Setenv("UT_AUTH", "off")

	req := httptest.NewRequest(http.MethodGet, "/menu", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{"العربية", "English", "فارسی", "Türkçe"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /menu language row missing native language name %q", want)
		}
	}
	// Anchored on the href this row actually renders, so the codes' other
	// legitimate uses on the page (the ?lang= values themselves) cannot
	// false-positive.
	for _, code := range []string{"ar", "en", "fa", "tr"} {
		if strings.Contains(body, `/menu?lang=`+code+`">`+code+`</a>`) {
			t.Errorf("GET /menu still renders bare locale code %q as a button label", code)
		}
	}
}
