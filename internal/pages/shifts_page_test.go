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

func newShiftsPageTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
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
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerShiftsPage(mux, dp)
	return mux, dp
}

func TestShiftsPage_NoOpenShiftShowsNoneOpenAndListsRegisters(t *testing.T) {
	mux, dp := newShiftsPageTestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/shifts", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No open shift") {
		t.Fatalf("expected the no-open-shift banner, got: %s", body)
	}
	if strings.Contains(body, "Current Shift") {
		t.Fatalf("expected no Current Shift card when nothing is open, got: %s", body)
	}
	if !strings.Contains(body, "Front Till") {
		t.Fatalf("expected the open-new-shift form to list the seeded register, got: %s", body)
	}
}

func TestShiftsPage_OpenShiftShowsCurrentAndHistory(t *testing.T) {
	mux, dp := newShiftsPageTestDeps(t)
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,opening_cash) VALUES('shift1','reg1','user1','2026-01-01T09:00:00Z',5000)`); err != nil {
		t.Fatal(err)
	}
	// closing_cash and expected_cash are deliberately DIFFERENT (1200 vs
	// 1500) so a template regression that swapped the Counted/Expected
	// columns would actually be caught, and so a non-zero variance is
	// observable.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,closed_at,opening_cash,closing_cash,expected_cash) VALUES('shift0','reg1','user1','2026-01-01T00:00:00Z','2026-01-01T08:00:00Z',1000,1200,1500)`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/shifts", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Shift open since") {
		t.Fatalf("expected the open-since banner, got: %s", body)
	}
	if !strings.Contains(body, "Current Shift") {
		t.Fatalf("expected the Current Shift card, got: %s", body)
	}
	if !strings.Contains(body, "reg1") {
		t.Fatalf("expected the open shift's register in the current-shift card, got: %s", body)
	}
	if !strings.Contains(body, "£50.00") {
		t.Fatalf("expected the open shift's opening cash rendered, got: %s", body)
	}
	if !strings.Contains(body, "£12.00") {
		t.Fatalf("expected the closed shift's counted (closing) cash in history, got: %s", body)
	}
	if !strings.Contains(body, "£15.00") {
		t.Fatalf("expected the closed shift's expected cash in history, got: %s", body)
	}
	// Variance = counted(1200) - expected(1500) = -300 minor units;
	// FormatMoney hugs the symbol to the number, so GBP renders this "£-3.00".
	if !strings.Contains(body, "£-3.00") {
		t.Fatalf("expected the closed shift's variance in history, got: %s", body)
	}
}
