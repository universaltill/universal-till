package pages

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
	sqlited "modernc.org/sqlite"
)

// TestImport_CategoryAndTaxCodeLookupsAreCachedPerRun is the ut-docs#1322
// regression (perf audit 2026-08-30-performance-audit.md section F,
// finding #3): EnsureCategoryUnder/FindOrCreateTaxCode used to be called
// once per row, so a batch import re-issued the identical department/
// category/tax-code SELECT for every row sharing a value — a 2,000-3,000
// row import across ~30 categories issued thousands of redundant lookups
// instead of ~30-60. A plain correctness test can't tell an N+1 shape from
// a cached one (both produce identical rows), so this opens a second
// connection to the same on-disk DB through a counting driver.Connector
// (same technique as internal/data's TestSalesForExport_ConstantQueryCount)
// that records every SELECT prepared, imports a batch where many rows
// share few distinct category/tax-rate values, and asserts the SELECT
// count tracks the number of distinct values, not the number of rows.
func TestImport_CategoryAndTaxCodeLookupsAreCachedPerRun(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	chdirRoot(t)

	path := filepath.Join(t.TempDir(), "import_querycount.db")
	seedDB, err := appdb.Open(path)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { _ = seedDB.Close() })

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, seedDB.DB)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	seedStore := settings.NewStore(seedDB.DB)
	if err := seedStore.Set(t.Context(), common.KeyCurrencyConfirmed, "true"); err != nil {
		t.Fatalf("seed currency confirmed: %v", err)
	}
	state := common.LoadState(t.Context(), seedStore, cfg)

	counter := new(int64)
	countingDB := openImportCountingConn(t, path, counter)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       countingDB,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(countingDB),
	}

	mux := http.NewServeMux()
	registerImport(mux, dp)

	// 40 rows across 4 distinct categories and 4 distinct tax rates. The
	// uncached shape issues (up to) one category SELECT and one tax-code
	// SELECT per row — 80 total; cached, it's one of each per distinct
	// value the first time it's seen (<=8 total) plus a small per-row
	// constant unrelated to this fix (item/stock/barcode lookups).
	cats := []string{"Drinks", "Snacks", "Bakery", "Produce"}
	// Deliberately avoid 5/20/0% — 001_init.sql pre-seeds "Reduced VAT"
	// (500bp), "Standard VAT" (2000bp) and "Zero-rated" (0bp), so a rate
	// matching one of those would find the pre-existing row instead of
	// creating an "Imported %" one, undercounting this assertion without
	// any caching being involved.
	rates := []int{8, 12, 16, 22}
	var csv strings.Builder
	csv.WriteString("Name,SKU,Price,Category,Tax rate,In stock\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&csv, "Item%02d,SKU%02d,1.50,%s,%d,0\n", i, i, cats[i%len(cats)], rates[i%len(rates)])
	}

	body, ct := multipartCSV(t, csv.String(), map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	var itemCount int
	if err := seedDB.DB.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&itemCount); err != nil || itemCount != 40 {
		t.Fatalf("items created = %d (err %v), want 40", itemCount, err)
	}
	var catCount int
	if err := seedDB.DB.QueryRow(`SELECT COUNT(*) FROM categories`).Scan(&catCount); err != nil {
		t.Fatalf("count categories: %v", err)
	}
	if catCount != len(cats) {
		t.Fatalf("categories created = %d, want %d (dedup broken)", catCount, len(cats))
	}
	var taxCount int
	if err := seedDB.DB.QueryRow(`SELECT COUNT(*) FROM tax_codes WHERE name LIKE 'Imported %'`).Scan(&taxCount); err != nil {
		t.Fatalf("count tax codes: %v", err)
	}
	if taxCount != len(rates) {
		t.Fatalf("tax codes created = %d, want %d (dedup broken)", taxCount, len(rates))
	}

	got := atomic.LoadInt64(counter)
	if got == 0 {
		t.Fatal("harness counted 0 categories/tax_codes SELECTs -- it stopped counting (assertion would be vacuous)")
	}
	// Every distinct category/rate needs exactly one categories/tax_codes
	// SELECT the first time it's seen (<=8 total: 4 categories + 4 rates).
	// The pre-fix, uncached shape issues one such SELECT per row instead —
	// up to 80 for 40 rows. Allow some slack above the theoretical minimum
	// (repeated top-level lookups, retry paths) without letting a
	// per-row-scaling regression pass.
	if got > int64(2*(len(cats)+len(rates))) {
		t.Fatalf("categories/tax_codes SELECT count %d suggests lookups are not cached per distinct value (40 rows, %d distinct categories + %d distinct rates, want roughly <=%d)", got, len(cats), len(rates), len(cats)+len(rates))
	}
}

// openImportCountingConn mirrors internal/data's export_repo_querycount_test.go
// countingConnector -- a second connection to the same on-disk DB that
// counts every SELECT prepared, so an N+1 regression is caught even though
// its output is byte-identical to the cached shape's.
func openImportCountingConn(t *testing.T, path string, counter *int64) *sql.DB {
	t.Helper()
	// Mirror internal/db.Open's pragmas exactly (not just the busy-timeout/
	// WAL subset export_repo_querycount_test.go's read-only variant uses):
	// this connection drives the import commit's actual writes (item
	// inserts, FK columns, a BeginTx per row), so a missing
	// foreign_keys(1)/_txlock=immediate here would let the test pass
	// against a laxer connection than production ever runs on
	// (independent review finding N1, ut-docs#1322).
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=temp_store(2)&_txlock=immediate", path)
	countingDB := sql.OpenDB(&importCountingConnector{dsn: dsn, driver: &sqlited.Driver{}, counter: counter})
	t.Cleanup(func() { _ = countingDB.Close() })
	return countingDB
}

type importCountingConnector struct {
	dsn     string
	driver  driver.Driver
	counter *int64
}

func (c *importCountingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &importCountingConn{Conn: conn, counter: c.counter}, nil
}

func (c *importCountingConnector) Driver() driver.Driver { return c.driver }

type importCountingConn struct {
	driver.Conn
	counter *int64
}

func (c *importCountingConn) Prepare(query string) (driver.Stmt, error) {
	c.count(query)
	return c.Conn.Prepare(query)
}

func (c *importCountingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.count(query)
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, query)
	}
	return c.Conn.Prepare(query)
}

// count only tallies SELECTs against the two tables this fix targets —
// the row loop issues plenty of other per-row SELECTs (items, barcodes,
// stock, plugin hooks) that scale with row count regardless of this fix
// and would swamp the signal if counted too.
func (c *importCountingConn) count(query string) {
	upper := strings.ToUpper(strings.TrimSpace(query))
	if !strings.HasPrefix(upper, "SELECT") {
		return
	}
	if strings.Contains(upper, "FROM CATEGORIES") || strings.Contains(upper, "FROM TAX_CODES") {
		atomic.AddInt64(c.counter, 1)
	}
}
