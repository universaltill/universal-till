package pos

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	sqlited "modernc.org/sqlite"
)

// openCountingSaleDB is a package-local copy of internal/data's
// export_repo_querycount_test.go openCountingConnMatching helper. It's
// unexported there, and package pos cannot import an unexported test symbol
// from package data — pos_repo_scanline_querycount_test.go already took the
// same copy-not-import approach for the same reason. It opens a
// driver.Connector wrapping modernc.org/sqlite's own driver.Conn, counting
// every statement whose text starts with `prefix` (case-insensitive) that
// gets prepared — see that file's countingConn doc comment for why counting
// Prepare/PrepareContext faithfully counts both QueryContext and
// ExecContext calls (database/sql always routes through Prepare for a
// connection that implements neither Execer nor Queryer, which this
// wrapper deliberately doesn't).
func openCountingSaleDB(t *testing.T, path string, counter *int64, prefix string) *sql.DB {
	t.Helper()
	countingDB := sql.OpenDB(&countingSaleConnector{path: path, driver: &sqlited.Driver{}, counter: counter, prefix: strings.ToUpper(prefix)})
	countingDB.SetMaxOpenConns(1)
	countingDB.SetMaxIdleConns(1)
	if _, err := countingDB.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable fks on counting connection: %v", err)
	}
	t.Cleanup(func() { _ = countingDB.Close() })
	return countingDB
}

type countingSaleConnector struct {
	path    string
	driver  driver.Driver
	counter *int64
	prefix  string
}

func (c *countingSaleConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.path)
	if err != nil {
		return nil, err
	}
	return &countingSaleConn{Conn: conn, counter: c.counter, prefix: c.prefix}, nil
}

func (c *countingSaleConnector) Driver() driver.Driver { return c.driver }

type countingSaleConn struct {
	driver.Conn
	counter *int64
	prefix  string
}

func (c *countingSaleConn) Prepare(query string) (driver.Stmt, error) {
	c.count(query)
	return c.Conn.Prepare(query)
}

func (c *countingSaleConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.count(query)
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, query)
	}
	return c.Conn.Prepare(query)
}

func (c *countingSaleConn) count(query string) {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), c.prefix) {
		atomic.AddInt64(c.counter, 1)
	}
}

// TestCompleteSale_INSERTCountDoesNotGrowLinearlyWithLineCount is
// ut-docs#2250, the regression guard #1347 item 2 deliberately deferred.
// #1318 batched CompleteSale's per-line repo writes (InsertSaleLinesBatch,
// InsertSaleLineModifiersBatch, InsertSaleDiscountsBatch,
// RecordStockMovementsBatch, CurrentQtyBatch); #1347 added chunk-boundary
// coverage for those batched methods themselves. Neither proves the
// BATCHING stays in place: sales_batch_test.go's suite only checks the
// written DATA is correct, and the #1318 review confirmed the OLD
// one-exec-per-line loop passes that same suite identically — nothing
// fails if a future edit quietly reintroduces it.
//
// This counts every INSERT statement CompleteSale actually prepares
// (mirrors internal/data/sync_admin_batch_test.go's
// TestAdminApplyINSERTCountDoesNotGrowLinearlyWithRowCount, ut-docs#1369 —
// same shape, one repo layer up) for a small and a much larger basket, and
// asserts the growth between them stays small and flat rather than
// tracking line count 1:1.
func TestCompleteSale_INSERTCountDoesNotGrowLinearlyWithLineCount(t *testing.T) {
	ctx := context.Background()

	countFor := func(n int) int64 {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("pos_insertcount_%d.db", n))
		// Seeding runs through its own connection, closed (via defer, so a
		// t.Fatalf mid-seed still closes it rather than leaking) BEFORE the
		// counting connection below ever opens — CompleteSale is measured
		// through a single connection with nothing else live against the
		// same file at that point.
		func() {
			setup := setupSaleDBAtPath(t, path)
			defer func() { _ = setup.Close() }()
			if _, err := setup.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`); err != nil {
				t.Fatalf("seed location: %v", err)
			}
			if _, err := setup.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`); err != nil {
				t.Fatalf("seed item: %v", err)
			}
			// Inventory headroom well above n so AllowNegativeInventory=false
			// never rejects the basket regardless of n.
			if _, err := setup.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',100000,datetime('now'))`); err != nil {
				t.Fatalf("seed inventory: %v", err)
			}
			if _, err := setup.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`); err != nil {
				t.Fatalf("seed payment method: %v", err)
			}
		}()

		counter := new(int64)
		countingDB := openCountingSaleDB(t, path, counter, "INSERT")

		// Every line also carries a modifier and a small line discount
		// (ut-docs#2229 review finding 1 mirrored here — every line in the
		// original draft left InsertSaleLineModifiersBatch/
		// InsertSaleDiscountsBatch as unconditional no-ops, since neither
		// modifiers nor a line discount was ever set, so a regression in
		// EITHER of those two batched calls specifically would have been
		// invisible to this guard). lineNet = 500 - 10 = 490/line.
		lines := make([]SaleLineInput, n)
		for i := range lines {
			lines[i] = SaleLineInput{
				ItemID: "itm1", SKU: "SKU1", Name: "Apple",
				Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 0,
				LineDiscount: money.FromMinor(10),
				LocationID:   "loc1",
				Modifiers: []data.SelectedModifier{
					{GroupID: "g-extras", OptionID: "o-shot", GroupName: "Extras", OptionName: "Extra shot", PriceDeltaMinor: 0},
				},
			}
		}
		in := SaleInput{
			SaleType:   "sale",
			RegisterID: "reg1",
			CashierID:  "user1",
			Currency:   "GBP",
			Lines:      lines,
			Payments: []PaymentInput{
				{MethodID: "cash", Amount: money.FromMinor(490 * int64(n)), Currency: "GBP"},
			},
		}
		saleID, err := CompleteSale(ctx, countingDB, in)
		if err != nil {
			t.Fatalf("CompleteSale (n=%d): %v", n, err)
		}
		if saleID == "" {
			t.Fatalf("expected a saleID (n=%d)", n)
		}
		return atomic.LoadInt64(counter)
	}

	small := countFor(2)
	large := countFor(50)

	// Guard against the harness silently counting nothing, same defense as
	// export_repo_querycount_test.go's own check — a genuine CompleteSale
	// always issues several INSERTs (sale header, lines batch, stock
	// movements batch, payment, audit), so 0 means the wrapper stopped
	// counting, not that CompleteSale got cheaper.
	if small == 0 || large == 0 {
		t.Fatalf("harness counted 0 INSERTs (small=%d, large=%d) — it stopped counting, assertions below would be vacuous", small, large)
	}

	growth := large - small
	// 48 more lines (50 vs 2) batched in chunks well under maxBatchParams
	// costs at most a handful more statements from chunk-boundary shifts;
	// 20 is a generous bound the OLD one-exec-per-line code (48 more
	// InsertSaleLine/InsertStockMovement/... calls) would blow through by
	// more than 2x, same margin sync_admin_batch_test.go's sibling test
	// uses for the same reasoning.
	if growth > 20 {
		t.Fatalf("INSERT count grew by %d for 48 extra basket lines (small=%d, large=%d) — looks like one exec per line again, not batched writes", growth, small, large)
	}
}
