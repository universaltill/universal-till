package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// newTablePickerTestDeps mirrors newSuggestionsTestDeps: registerTablePicker
// reads the real `tables`/held_sales schema (migration 054), not in the
// simplified seedForPages fixture, so this uses a fully migrated database.
func newTablePickerTestDeps(t *testing.T, engine *pos.Service) *common.Deps {
	t.Helper()
	chdirRoot(t)
	d, err := appdb.Open(filepath.Join(t.TempDir(), "table-picker.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20}}
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
		Engine:   engine,
		Pm:       pm,
		Settings: settings.NewStore(d.DB),
	}
}

func TestTablePicker_ListsFreeTablesOnly(t *testing.T) {
	engine := pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	dp := newTablePickerTestDeps(t, engine)
	repo := data.NewPOSRepo(dp.Db)
	ctx := context.Background()

	free, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T1: %v", err)
	}
	busy, err := repo.CreateTable(ctx, "T2", "", 4, "rect", 300, 100)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','',0,0,'{}',?)`, busy); err != nil {
		t.Fatalf("seed occupying held sale: %v", err)
	}

	mux := http.NewServeMux()
	registerTablePicker(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/pos/table-picker", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/pos/table-picker: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "T1") {
		t.Fatalf("expected the free table T1 listed, got %s", body)
	}
	if strings.Contains(body, "T2") {
		t.Fatalf("expected the occupied table T2 NOT listed, got %s", body)
	}
	if !strings.Contains(body, `hx-post="/api/pos/table"`) {
		t.Fatalf("expected the option to POST to /api/pos/table, got %s", body)
	}
	if !strings.Contains(body, free) {
		t.Fatalf("expected the free table's id in the picker markup, got %s", body)
	}
}

// The table the CURRENT basket is already assigned to must still be listed
// even if it happens to be "occupied" (e.g. by a held sale on the same
// table) -- otherwise re-opening the picker couldn't show the current
// choice at all.
func TestTablePicker_IncludesCurrentlyAssignedTableEvenIfOccupied(t *testing.T) {
	engine := pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	dp := newTablePickerTestDeps(t, engine)
	repo := data.NewPOSRepo(dp.Db)
	ctx := context.Background()

	current, err := repo.CreateTable(ctx, "T3", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T3: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','',0,0,'{}',?)`, current); err != nil {
		t.Fatalf("seed occupying held sale: %v", err)
	}
	dp.Engine.SetTable(current, "T3")

	mux := http.NewServeMux()
	registerTablePicker(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/pos/table-picker", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/pos/table-picker: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "T3") {
		t.Fatalf("expected the currently-assigned table T3 still listed, got %s", body)
	}
}

func TestTablePicker_NoFreeTablesShowsEmptyState(t *testing.T) {
	engine := pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	dp := newTablePickerTestDeps(t, engine)

	mux := http.NewServeMux()
	registerTablePicker(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/pos/table-picker", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/pos/table-picker: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), httpx.T("en", "basket.table.no_free_tables")) {
		t.Fatalf("expected the no-free-tables copy when no tables exist, got %s", rec.Body.String())
	}
}
