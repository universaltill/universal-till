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
}
