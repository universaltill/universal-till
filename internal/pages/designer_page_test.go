package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

// registerDesigner renders the shortcut-button designer, backed by the
// shortcut_buttons table (not in seedForPages), so use a migrated database.
func newDesignerTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	d, err := appdb.Open(filepath.Join(t.TempDir(), "designer.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cfg := &config.Config{Theme: "monarch", Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, d.DB)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(d.DB), cfg)
	return &common.Deps{
		Cfg:      cfg,
		Db:       d.DB,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		BtnStore: ui.NewButtonStore(d.DB),
		Pm:       pm,
		Settings: settings.NewStore(d.DB),
	}
}

func TestDesigner_RendersPage(t *testing.T) {
	dp := newDesignerTestDeps(t)
	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The designer page composes the buttons_admin partial.
	if !strings.Contains(body, "designer") {
		t.Fatalf("expected the designer container, got %s", body)
	}
}

func TestDesigner_RendersSeededButtons(t *testing.T) {
	dp := newDesignerTestDeps(t)

	// The migrated schema seeds sample shortcut buttons; add a deterministic one
	// against a known item so the grid renders its label through ToVM.
	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmZ','ZZZ','Zephyr Widget',500,1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if err := dp.BtnStore.Add(ui.Button{Label: "Zephyr Tile", Code: "ZZZ", ItemID: "itmZ"}); err != nil {
		t.Fatalf("add button: %v", err)
	}

	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Zephyr Tile") {
		t.Fatalf("expected the seeded button label rendered in the grid, got %s", rec.Body.String())
	}
}
