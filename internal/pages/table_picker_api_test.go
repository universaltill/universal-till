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

// A shop with NO tables configured must get NO table chrome at all -- not a
// "no free tables" message, not a "Table: none" row. ADR-0054 soft-gates the
// whole feature on configuration ("zero tables configured = today's behavior
// exactly"); rendering an always-on row here instead cost two rows of basket
// height on every till and broke the sale-screen-213 / phone-width-413 layout
// assertions (ut-docs#820 review).
func TestTablePicker_NoTablesConfiguredRendersNothing(t *testing.T) {
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
	body := rec.Body.String()
	if strings.Contains(body, httpx.T("en", "basket.table.no_free_tables")) {
		t.Fatalf("no-tables shop must not show the no-free-tables message, got %s", body)
	}
	if strings.Contains(body, httpx.T("en", "basket.table.label")) {
		t.Fatalf("no-tables shop must not show the Table: label row, got %s", body)
	}
	if strings.Contains(body, "table-picker-option") || strings.Contains(body, "table-picker-title") {
		t.Fatalf("no-tables shop must not show any picker option, got %s", body)
	}
}

// Assigning a physical table to a takeaway/to-go order doesn't make sense
// (ut-docs#1355) -- with the basket's order type set to Takeaway, the picker
// must render nothing at all, the same "no chrome" shape as the
// no-tables-configured case above, even though tables ARE configured and
// free.
func TestTablePicker_HiddenForTakeaway(t *testing.T) {
	engine := pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	dp := newTablePickerTestDeps(t, engine)
	repo := data.NewPOSRepo(dp.Db)
	ctx := context.Background()

	if _, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100); err != nil {
		t.Fatalf("CreateTable T1: %v", err)
	}
	dp.Engine.SetOrderType(pos.OrderTypeTakeaway)

	mux := http.NewServeMux()
	registerTablePicker(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ui/pos/table-picker", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/pos/table-picker: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "table-picker-trigger") || strings.Contains(body, "table-modal") {
		t.Fatalf("expected no table chrome at all while order type is takeaway, got %s", body)
	}
	if strings.Contains(body, "T1") {
		t.Fatalf("expected the configured table not to appear while order type is takeaway, got %s", body)
	}
}

// A shop that DOES have tables but they're all occupied is a real, distinct
// state that DOES warrant the "no free tables" message (unlike the no-tables
// case above) -- so the operator knows why there's nothing to pick, rather
// than seeing the table row silently vanish.
func TestTablePicker_ConfiguredButAllOccupiedShowsEmptyState(t *testing.T) {
	engine := pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
	dp := newTablePickerTestDeps(t, engine)
	repo := data.NewPOSRepo(dp.Db)
	ctx := context.Background()

	busy, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T1: %v", err)
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
	if !strings.Contains(rec.Body.String(), httpx.T("en", "basket.table.no_free_tables")) {
		t.Fatalf("expected the no-free-tables copy when all tables are occupied, got %s", rec.Body.String())
	}
}
