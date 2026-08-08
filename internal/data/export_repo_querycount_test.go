package data

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	sqlited "modernc.org/sqlite"
)

// TestSalesForExport_ConstantQueryCount is the ut-docs#229 regression:
// SalesForExport used to run 1 (sales) + 2 per matched sale (tax lines,
// payments) queries — a plain correctness test can't tell an N+1 shape
// from a joined one, since both produce identical output. This opens a
// second connection to the same on-disk DB through a counting
// driver.Connector that records every SELECT prepared, seeds a batch of
// sales, and asserts the count stays flat as the batch grows — proving
// the query count is independent of the number of matched sales, not just
// checking a single fixed number.
func TestSalesForExport_ConstantQueryCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pos_querycount.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	for _, tbl := range []string{"payments", "sale_links", "sale_discounts", "sale_lines", "sales", "shifts", "shortcut_buttons", "inventory"} {
		if _, err := d.DB.ExecContext(ctx, `DELETE FROM `+tbl); err != nil {
			t.Fatalf("clear seeded %s: %v", tbl, err)
		}
	}
	dbx := &posTestDB{d: d, repo: NewPOSRepo(d.DB)}
	seedExportTestItem(t, dbx)

	// Each size gets its own day and ID prefix so the two batches never
	// collide (unique receipt_no) or leak into each other's date-ranged
	// query (each call only ever sees the sales it just seeded).
	countFor := func(n int, day, prefix string) int64 {
		// Reopen a fresh counting connection per size: WAL keeps the two
		// connections consistent, but a clean counter per subtest avoids
		// carrying the previous size's Prepare calls into this one's total.
		counter := new(int64)
		countingDB := openCountingConn(t, path, counter)
		repo := NewPOSRepo(countingDB)

		for i := 0; i < n; i++ {
			id := fmt.Sprintf("qc-sale-%s-%d", prefix, i)
			seedExportSale(t, dbx, id, fmt.Sprintf("%s%d", prefix, i), "sale", day+"T09:00:00Z", 1000, 200, 2000, "cash", 1200)
		}

		got, err := repo.SalesForExport(ctx, day, day)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != n {
			t.Fatalf("expected %d sales, got %d", n, len(got))
		}
		return atomic.LoadInt64(counter)
	}

	small := countFor(2, "2026-06-15", "SM")
	large := countFor(50, "2026-06-16", "LG")

	// Independent review finding (2026-08-08): without a lower bound, a
	// counting harness that silently stops counting (e.g. a future
	// "helpful" tidy-up that lets QueryerContext bypass the wrapped
	// Prepare/PrepareContext path) reports small==large==0 and this test
	// would pass even with the N+1 bug fully restored — verified by
	// actually doing that swap against this exact test. 3 is the real
	// count for a non-empty match (sales + tax-lines-batch +
	// payments-batch); guard against the harness going quiet, not just
	// against the count growing.
	if large < 3 {
		t.Fatalf("harness counted %d SELECTs for 50 sales -- it stopped counting (assertion would be vacuous), not that the query count is actually small", large)
	}

	// The old N+1 shape issues 1 + 2*n SELECTs (5 for n=2, 101 for n=50) --
	// a query count that visibly grows with n. The joined shape issues a
	// small constant number (sales + tax-lines-batch + payments-batch)
	// regardless of n.
	if small != large {
		t.Fatalf("query count grew with sale count: %d sales -> %d queries, %d sales -> %d queries (want equal, constant)", 2, small, 50, large)
	}
	if large > 5 {
		t.Fatalf("expected a small constant number of SELECTs, got %d for 50 sales", large)
	}
}

// openCountingConn opens a second *sql.DB against the same on-disk SQLite
// file (already migrated by db.Open) through a driver.Connector that wraps
// modernc.org/sqlite's own driver.Conn, counting every SELECT statement
// prepared. WAL mode (set on the file by db.Open's DSN) makes a second
// connection to the same path safe to read from concurrently.
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
