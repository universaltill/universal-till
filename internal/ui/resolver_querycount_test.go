package ui

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	sqlited "modernc.org/sqlite"
)

// TestResolve_DoesNotScaleWithShape is the ut-docs#1660 regression: before
// this card, PriceResolverAdapter.Resolve probed a.resolve (backed by
// POSRepo.ResolveShortcutLineDecoded) once per candidate shape it checked
// (variant, then item, then shortcut, then a SKU/name fallback) and
// discarded every result whose shape didn't match -- up to 4 identical
// round trips for one Resolve call, since a.resolve is a pure,
// deterministic lookup (same code, same row, every time). A plain
// correctness test can't see this: every shape still returns the same
// resolved line either way. So, same technique as
// internal/data/export_repo_querycount_test.go's
// TestSalesForExport_ConstantQueryCount (package-private there,
// unreachable from here, hence this local copy): open a real on-disk
// SQLite file through a driver.Connector that counts every SELECT
// prepared, and assert that ONE Resolve call costs exactly the same
// number of SELECTs as ONE direct a.Store.posRepo.ResolveShortcutLineDecoded
// call -- for every shape Resolve can return, not just one. The exact
// per-shape cost is intentionally not pinned here (it's
// ResolveShortcutLineDecoded's own concern, and already varies by shape
// for reasons unrelated to this card); what this proves is that Resolve
// no longer multiplies it.
func TestResolve_DoesNotScaleWithShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolver_querycount.db")
	counter := new(int64)
	countingDB := openCountingConn(t, path, counter)
	seedQCResolverFixture(t, countingDB)

	store := NewButtonStore(countingDB)
	r := PriceResolverAdapter{Store: store}
	ctx := context.Background()

	// Independent-review guard (same reasoning as
	// TestSalesForExport_ConstantQueryCount / TestResolveScanLine_QueryCount):
	// a harness that silently stops counting would make every comparison
	// below vacuously true, so require at least one SELECT was actually
	// observed on every hit path before trusting the equality.
	requireCounted := func(t *testing.T, got int64) {
		t.Helper()
		if got < 1 {
			t.Fatalf("harness counted %d SELECTs -- it stopped counting (assertion would be vacuous)", got)
		}
	}

	baselineFor := func(code string) int64 {
		atomic.StoreInt64(counter, 0)
		_, _, _ = store.posRepo.ResolveShortcutLineDecoded(ctx, code)
		return atomic.LoadInt64(counter)
	}
	resolveCountFor := func(code string) (int64, bool) {
		atomic.StoreInt64(counter, 0)
		_, ok := r.Resolve(code)
		return atomic.LoadInt64(counter), ok
	}

	cases := []struct {
		name string
		code string
		want bool // whether a.Store.posRepo.ResolveShortcutLineDecoded(code) hits
	}{
		{"variant barcode hit", "VB-1", true},
		{"item barcode hit", "IB-1", true},
		{"shortcut barcode hit (no item/variant id)", "SC-1", true},
		{"SKU exact-match fallback hit", "BAN-SKU", true},
		{"name-LIKE fallback hit", "Banan", true},
		{"miss", "no-such-code", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := baselineFor(tc.code)
			got, ok := resolveCountFor(tc.code)
			if ok != tc.want {
				t.Fatalf("Resolve(%q) ok = %v, want %v", tc.code, ok, tc.want)
			}
			if tc.want {
				requireCounted(t, base)
				requireCounted(t, got)
			}
			if got != base {
				t.Fatalf("Resolve(%q): %d SELECTs, want exactly %d (one direct ResolveShortcutLineDecoded call) -- the resolve chain is being probed more than once", tc.code, got, base)
			}
		})
	}
}

// seedQCResolverFixture mirrors resolver_test.go's seedResolverFixture, but
// against an already-open *sql.DB (the counting connection) instead of
// building its own -- resolver_test.go's helper always opens a fresh
// :memory: DB via setupFullTestDB, which this test can't route through the
// counting connector.
func seedQCResolverFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT, name TEXT, base_price INTEGER NOT NULL, tax_code_id TEXT, category_id TEXT, is_active INTEGER NOT NULL DEFAULT 1, is_weighed INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE categories (id TEXT PRIMARY KEY, name TEXT NOT NULL, parent_id TEXT, sort_order INTEGER NOT NULL DEFAULT 0, color TEXT);`,
		`CREATE TABLE item_images (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, role TEXT NOT NULL, path TEXT NOT NULL);`,
		`CREATE TABLE price_history (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, price INTEGER NOT NULL, starts_at TEXT NOT NULL, ends_at TEXT);`,
		`CREATE TABLE item_barcodes (barcode TEXT PRIMARY KEY, item_id TEXT NOT NULL, is_primary INTEGER DEFAULT 0);`,
		`CREATE TABLE variant_barcodes (barcode TEXT PRIMARY KEY, variant_id TEXT NOT NULL, is_primary INTEGER DEFAULT 0);`,
		`CREATE TABLE shortcut_buttons (barcode TEXT PRIMARY KEY, label TEXT, item_id TEXT, image_path TEXT, sort_order INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE tax_codes (id TEXT PRIMARY KEY, rate_basis_points INTEGER NOT NULL, takeaway_rate_basis_points INTEGER);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, name TEXT, price INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_modifier_groups (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, name TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}

	// Same fixture shape as resolver_test.go's seedResolverFixture: a
	// variant barcode, an item barcode, a shortcut-only barcode, a SKU, and
	// a name -- one code per Resolve shape.
	mustExec(t, db, `INSERT INTO tax_codes(id, rate_basis_points) VALUES('tax-std', 2000)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, tax_code_id, is_active, is_weighed) VALUES('itm-cof','COF-SKU','Coffee', 300, 'tax-std', 1, 0)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, is_weighed) VALUES('itm-ban','BAN-SKU','Bananas', 89, 1, 1)`)
	mustExec(t, db, `INSERT INTO item_variants(id, item_id, name, price, is_active) VALUES('var-lg','itm-cof','Large', 400, 1)`)
	mustExec(t, db, `INSERT INTO variant_barcodes(barcode, variant_id, is_primary) VALUES('VB-1','var-lg',1)`)
	mustExec(t, db, `INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('IB-1','itm-cof',1)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('SC-1','Loose Bananas','itm-ban',0)`)
}

// openCountingConn opens a *sql.DB against an on-disk SQLite file through a
// driver.Connector that wraps modernc.org/sqlite's own driver.Conn,
// counting every SELECT statement prepared -- same technique as
// internal/data/export_repo_querycount_test.go's helper of the same name
// (package-private there, unreachable from this package, hence this local
// copy). Unlike that helper, this one owns the only connection to the file
// (there's no separately-opened "real" connection alongside it) -- it's
// both the DB Resolve runs against and the one being counted.
func openCountingConn(t *testing.T, path string, counter *int64) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	countingDB := sql.OpenDB(&countingConnector{dsn: dsn, driver: &sqlited.Driver{}, counter: counter})
	t.Cleanup(func() { _ = countingDB.Close() })
	return countingDB
}

type countingConnector struct {
	dsn     string
	driver  driver.Driver
	counter *int64
}

func (c *countingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, counter: c.counter}, nil
}

func (c *countingConnector) Driver() driver.Driver { return c.driver }

// countingConn wraps a driver.Conn, counting every SELECT statement
// prepared. *sql.DB.QueryContext prepares a fresh statement per call (no
// caching) when called directly rather than through a pre-built *sql.Stmt,
// so counting Prepare/PrepareContext calls for SELECT text faithfully
// counts application-level QueryContext calls.
type countingConn struct {
	driver.Conn
	counter *int64
}

func (c *countingConn) Prepare(query string) (driver.Stmt, error) {
	c.count(query)
	return c.Conn.Prepare(query)
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.count(query)
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, query)
	}
	return c.Conn.Prepare(query)
}

func (c *countingConn) count(query string) {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT") {
		atomic.AddInt64(c.counter, 1)
	}
}
