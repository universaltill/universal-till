package ui

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	sqlited "modernc.org/sqlite"
)

// TestButtonsHTTPList_LoadAllActiveRunsOnceNotTwice is the ut-docs#2541
// review finding 2 regression: ButtonsHTTP.List used to call
// Store.LoadAllActive TWICE per render -- once indirectly, inside the old
// Store.Load()'s own body (the implicit-tile merge always needs every
// active item, in every mode), and a second time directly for the
// All grid. LoadAllActive's own query (CatalogRepo.ListItems --
// the one unconditionally run at its top, before any of its batched/
// chunked follow-up lookups) is the cheapest reliable fingerprint for "did
// LoadAllActive run:" it has a distinctive, stable SQL fragment
// (`FROM items WHERE is_active = 1 ORDER BY name`) that no other query this
// render issues (LoadCategories/LoadCategoriesForAdmin only ever query
// `categories`). A plain correctness test can't see this doubling at all --
// both the old and new code paths render an identical body -- so this counts
// how many times that exact fragment is prepared during ONE List() call and
// asserts it's exactly 1 in every browsing mode: all_filter_chips (the one
// mode that still renders an All grid — the doubling case) and the strip,
// which since ut-docs#2613 has no All grid (LoadAllActive still has to run
// once, for Load's own implicit merge), plus category_tabs' tiles.
func TestButtonsHTTPList_LoadAllActiveRunsOnceNotTwice(t *testing.T) {
	// The distinctive fragment from CatalogRepo.ListItems' own query --
	// see internal/data/catalog_repo.go's ListItems. Kept as a literal here
	// (not imported) so this test still catches a future accidental second
	// call even if ListItems' own SQL text is edited to match, as long as
	// this substring survives; if it doesn't, the "at least 1" guard below
	// still catches a harness that silently stopped counting.
	const listItemsFragment = "FROM items WHERE is_active = 1 ORDER BY name"

	runOnce := func(t *testing.T, mode string) int64 {
		t.Helper()
		path := filepath.Join(t.TempDir(), "buttons_list_querycount.db")
		counter := new(int64)
		countingDB := openMatchingCountingConn(t, path, counter, listItemsFragment)
		seedQCAllTabFixture(t, countingDB, 12)

		store := NewButtonStore(countingDB)
		renderer, err := NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons.html"),
			httpx.FuncsFor("en"),
		)
		if err != nil {
			t.Fatalf("NewRenderer: %v", err)
		}
		h := &ButtonsHTTP{Store: *store, View: renderer, BrowsingMode: mode}

		atomic.StoreInt64(counter, 0)
		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
		if rec.Code != 200 {
			t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
		}
		return atomic.LoadInt64(counter)
	}

	for _, mode := range []string{browsingModeAllFilterChips, browsingModeStripOverflow, browsingModeCategoryTabs} {
		if got := runOnce(t, mode); got != 1 {
			t.Fatalf("%s: LoadAllActive's own query ran %d times in one List() render, want exactly 1 (it must be fetched once and reused for the implicit-tile merge and the mode's own view)", mode, got)
		}
	}
}

// openMatchingCountingConn is openCountingConn (resolver_querycount_test.go)
// narrowed to count only SELECTs whose text contains substr -- this test
// needs to isolate LoadAllActive's own fingerprint query from every other
// SELECT a full List() render issues (categories, settings, shortcut_buttons,
// barcodes, thumbnails, modifiers, variants, prices, ...), which the plain
// whole-render counter (openCountingConn/TestButtonsHTTPList_AllTabDoesNotScaleWithCatalogSize)
// has no way to do.
func openMatchingCountingConn(t *testing.T, path string, counter *int64, substr string) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	countingDB := sql.OpenDB(&matchingCountingConnector{dsn: dsn, driver: &sqlited.Driver{}, counter: counter, substr: substr})
	t.Cleanup(func() { _ = countingDB.Close() })
	return countingDB
}

type matchingCountingConnector struct {
	dsn     string
	driver  driver.Driver
	counter *int64
	substr  string
}

func (c *matchingCountingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &matchingCountingConn{Conn: conn, counter: c.counter, substr: c.substr}, nil
}

func (c *matchingCountingConnector) Driver() driver.Driver { return c.driver }

type matchingCountingConn struct {
	driver.Conn
	counter *int64
	substr  string
}

func (c *matchingCountingConn) Prepare(query string) (driver.Stmt, error) {
	c.count(query)
	return c.Conn.Prepare(query)
}

func (c *matchingCountingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.count(query)
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, query)
	}
	return c.Conn.Prepare(query)
}

func (c *matchingCountingConn) count(query string) {
	if strings.Contains(query, c.substr) {
		atomic.AddInt64(c.counter, 1)
	}
}
