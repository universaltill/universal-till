package ui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2501: the sell-screen tile cache, driven through the real
// ButtonsHTTP handlers against a real migrated database (migration 023's and
// 042's triggers are what invalidate it, so a hand-rolled schema would prove
// nothing). Every SELECT is counted, so "served from cache" is asserted as
// "the catalog was not queried", not inferred from identical bytes.

type sellScreenFixture struct {
	db      *sql.DB
	counter *int64
	store   *ButtonStore
	cache   *SellScreenCache
}

func newSellScreenFixture(t *testing.T) *sellScreenFixture {
	t.Helper()
	path := testsupport.MigratedDBFile(t, "sellscreen.db")
	counter := new(int64)
	db := openCountingConn(t, path, counter)
	// memAvailable nil: these tests must not depend on the host's
	// /proc/meminfo (a CI box under memory pressure would refuse every Put).
	// The memory floor has its own fake-reader tests in sellscreen_cache_test.go.
	cache := newSellScreenCache(sellScreenCacheOptions{
		now:           time.Now,
		memAvailable:  nil,
		budgetBytes:   sellScreenCacheBudgetBytes,
		maxEntries:    sellScreenCacheMaxEntries,
		maxAge:        sellScreenCacheMaxAge,
		memFloorBytes: sellScreenMemFloorBytes,
		memCheckEvery: sellScreenMemCheckEvery,
	})
	f := &sellScreenFixture{db: db, counter: counter, store: NewButtonStore(db), cache: cache}
	f.exec(t, `INSERT INTO categories (id, name) VALUES ('cat-drinks', 'Drinks'), ('cat-food', 'Food')`)
	f.exec(t, `INSERT INTO items (id, sku, name, base_price, category_id) VALUES
		('itm-cola', 'COLA', 'Cola Can', 120, 'cat-drinks'),
		('itm-tea', 'TEA', 'Green Tea', 250, 'cat-drinks'),
		('itm-bun', 'BUN', 'Sticky Bun', 300, 'cat-food')`)
	// A till's very first sell-screen render lazily seeds a default setting
	// (a one-time settings INSERT, which bumps sync_admin_version), so the
	// entry that render stores is keyed on the pre-seed version and misses
	// once — correct (the version is read BEFORE the render), but noise for
	// these tests. Do that one-time render uncached here.
	warm := f.handler(t, false, false)
	warm.Cache = nil
	f.list(t, warm)
	return f
}

func (f *sellScreenFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func (f *sellScreenFixture) handler(t *testing.T, granted, edit bool) *ButtonsHTTP {
	t.Helper()
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", "buttons.html"),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	return &ButtonsHTTP{
		Store: *f.store, View: renderer, Cache: f.cache, Locale: "en",
		BrowsingMode: browsingModeStripOverflow, Granted: granted, EditMode: edit,
	}
}

// list runs one GET /ui/buttons and returns the body and how many SELECTs it
// cost.
func (f *sellScreenFixture) list(t *testing.T, h *ButtonsHTTP) (string, int64) {
	t.Helper()
	atomic.StoreInt64(f.counter, 0)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String(), atomic.LoadInt64(f.counter)
}

func (f *sellScreenFixture) category(t *testing.T, h *ButtonsHTTP, id string) (string, int64) {
	t.Helper()
	atomic.StoreInt64(f.counter, 0)
	rec := httptest.NewRecorder()
	h.CategoryItems(rec, httptest.NewRequest("GET", "/ui/buttons/category?id="+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("CategoryItems = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String(), atomic.LoadInt64(f.counter)
}

func TestSellScreenCache_SecondRequestServedWithoutCatalogQueries(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, false, false)
	first, firstQueries := f.list(t, h)
	if !strings.Contains(first, "Cola Can") {
		t.Fatalf("first render has no Cola Can tile:\n%s", first)
	}
	if firstQueries < 5 {
		t.Fatalf("first (uncached) render ran only %d SELECTs — the counting harness isn't counting", firstQueries)
	}
	second, secondQueries := f.list(t, h)
	if second != first {
		t.Fatal("cached response differs from the render it was stored from")
	}
	if secondQueries != 1 {
		t.Fatalf("cached request ran %d SELECTs, want exactly 1 (the version read)", secondQueries)
	}

	// Same for the category popup, keyed per category.
	drinks, _ := f.category(t, h, "cat-drinks")
	if !strings.Contains(drinks, "Cola Can") || strings.Contains(drinks, "Sticky Bun") {
		t.Fatalf("drinks popup content wrong:\n%s", drinks)
	}
	drinks2, q := f.category(t, h, "cat-drinks")
	if drinks2 != drinks || q != 1 {
		t.Fatalf("second drinks popup: %d SELECTs (want 1), identical=%v", q, drinks2 == drinks)
	}
	food, q := f.category(t, h, "cat-food")
	if q == 1 || !strings.Contains(food, "Sticky Bun") || strings.Contains(food, "Cola Can") {
		t.Fatalf("food popup was served from the drinks entry (%d SELECTs):\n%s", q, food)
	}
}

// The regression the whole design exists to prevent: a stale cache must
// never keep offering an item that was deactivated or deleted.
func TestSellScreenCache_DeactivatedItemDisappearsOnNextRequest(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, false, false)
	if body, _ := f.list(t, h); !strings.Contains(body, "Green Tea") {
		t.Fatal("Green Tea missing before deactivation")
	}
	if body, _ := f.category(t, h, "cat-drinks"); !strings.Contains(body, "Green Tea") {
		t.Fatal("Green Tea missing from the drinks popup before deactivation")
	}
	// The catalog's own deactivation (ut-docs#2698: the jiggle-mode trash
	// badge no longer deactivates -- see the remove-from-quick-buttons case
	// below).
	f.exec(t, `UPDATE items SET is_active = 0 WHERE id = 'itm-tea'`)
	if body, _ := f.list(t, h); strings.Contains(body, "Green Tea") {
		t.Fatal("deactivated Green Tea still offered on the sell screen (stale cache)")
	}
	if body, _ := f.category(t, h, "cat-drinks"); strings.Contains(body, "Green Tea") {
		t.Fatal("deactivated Green Tea still offered in the drinks popup (stale cache)")
	}
	// A hard delete too.
	f.exec(t, `DELETE FROM items WHERE id = 'itm-bun'`)
	if body, _ := f.list(t, h); strings.Contains(body, "Sticky Bun") {
		t.Fatal("deleted Sticky Bun still offered (stale cache)")
	}
	// ut-docs#2698: the trash badge's remove-from-quick-buttons (an items
	// flag + a shortcut_buttons delete) must invalidate too.
	if body, _ := f.list(t, h); !strings.Contains(body, "Cola Can") {
		t.Fatal("Cola Can missing before removal")
	}
	if err := f.store.RemoveFromQuickButtons(context.Background(), "itm-cola"); err != nil {
		t.Fatalf("RemoveFromQuickButtons: %v", err)
	}
	if body, _ := f.list(t, h); strings.Contains(body, "Cola Can") {
		t.Fatal("removed Cola Can still on the sell screen (stale cache)")
	}
}

func TestSellScreenCache_PriceHistoryChangeShowsNewPrice(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, false, false)
	oldPrice, newPrice := httpx.FormatMoney(120, "en"), httpx.FormatMoney(99, "en")
	if body, _ := f.list(t, h); !strings.Contains(body, oldPrice) || strings.Contains(body, newPrice) {
		t.Fatalf("before override: want %s and no %s", oldPrice, newPrice)
	}
	// price_history is not an admin table — only migration 042's trigger
	// makes this visible to the cache.
	f.exec(t, `INSERT INTO price_history (id, item_id, price, starts_at) VALUES ('ph1', 'itm-cola', 99, datetime('now', '-1 minute'))`)
	if body, _ := f.list(t, h); !strings.Contains(body, newPrice) {
		t.Fatalf("after a price_history override the sell screen still lacks %s (stale cache)", newPrice)
	}
}

func TestSellScreenCache_ItemImageChangeShowsNewThumbnail(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, false, false)
	f.list(t, h)
	f.exec(t, `INSERT INTO item_images (id, item_id, role, path) VALUES ('img1', 'itm-cola', 'thumbnail', '/public/images/cola-2501.png')`)
	if body, _ := f.list(t, h); !strings.Contains(body, "/public/images/cola-2501.png") {
		t.Fatal("a new item thumbnail did not reach the sell screen (stale cache)")
	}
}

func TestSellScreenCache_ScheduledPriceGoesLiveAtBoundary(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, false, false)
	newPrice := httpx.FormatMoney(77, "en")
	// A price that starts ~2s from now: no write happens when it goes live,
	// so only the entry's price-boundary expiry can pick it up.
	start := time.Now().UTC().Truncate(time.Second).Add(2 * time.Second)
	f.exec(t, `INSERT INTO price_history (id, item_id, price, starts_at) VALUES ('ph-future', 'itm-cola', 77, ?)`, start.Format("2006-01-02 15:04:05"))
	body, _ := f.list(t, h)
	if strings.Contains(body, newPrice) {
		t.Fatalf("scheduled price %s shown before it starts", newPrice)
	}
	if time.Now().Before(start) {
		if _, q := f.list(t, h); q != 1 {
			t.Fatalf("before the boundary the entry should still be a hit (1 SELECT), got %d", q)
		}
	}
	time.Sleep(time.Until(start) + 100*time.Millisecond)
	if body, _ := f.list(t, h); !strings.Contains(body, newPrice) {
		t.Fatalf("scheduled price %s still not shown after its start (entry outlived the price boundary)", newPrice)
	}
}

func TestSellScreenCache_EditModeNeverCached(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, true, true)
	f.list(t, h)
	_, q := f.list(t, h)
	if q <= 1 {
		t.Fatalf("second edit-mode render ran %d SELECTs — it was served from cache", q)
	}
	if f.cache.Len() != 0 {
		t.Fatalf("edit mode stored %d cache entries, want 0", f.cache.Len())
	}
}

func TestSellScreenCache_GrantedAndNotGrantedGetTheirOwnHTML(t *testing.T) {
	f := newSellScreenFixture(t)
	cashier := f.handler(t, false, false)
	manager := f.handler(t, true, false)
	cashierBody, _ := f.list(t, cashier)
	managerBody, _ := f.list(t, manager)
	if !strings.Contains(cashierBody, `data-testid="tile-badge-lock-edit"`) {
		t.Fatal("cashier render has no lock badge")
	}
	if strings.Contains(managerBody, `data-testid="tile-badge-lock-edit"`) {
		t.Fatal("manager was served the cashier's cached HTML (lock badges)")
	}
	// Both now cached, each served its own.
	if again, q := f.list(t, cashier); again != cashierBody || q != 1 {
		t.Fatalf("cashier re-request: %d SELECTs, identical=%v", q, again == cashierBody)
	}
	if again, q := f.list(t, manager); again != managerBody || q != 1 {
		t.Fatalf("manager re-request: %d SELECTs, identical=%v", q, again == managerBody)
	}
}

func TestSellScreenCache_MissingCounterRowNeverCaches(t *testing.T) {
	f := newSellScreenFixture(t)
	f.exec(t, `DELETE FROM sell_screen_version`)
	h := f.handler(t, false, false)
	f.list(t, h)
	if _, q := f.list(t, h); q <= 1 {
		t.Fatalf("with no sell_screen_version row the second render ran %d SELECTs — cached anyway", q)
	}
	if f.cache.Len() != 0 {
		t.Fatalf("cache stored %d entries without a trustworthy version", f.cache.Len())
	}
}

// failingView writes part of a fragment and then fails, as a template
// execution error mid-render does.
type failingView struct{}

func (failingView) Render(w http.ResponseWriter, _ string, _ any) error {
	_, _ = fmt.Fprint(w, "<div>partial")
	return errors.New("template exploded")
}

func TestSellScreenCache_RenderErrorNotCached(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, false, false)
	h.View = failingView{}
	f.list(t, h)
	if f.cache.Len() != 0 {
		t.Fatalf("a failed render was cached (%d entries)", f.cache.Len())
	}
	rec := httptest.NewRecorder()
	h.CategoryItems(rec, httptest.NewRequest("GET", "/ui/buttons/category?id=cat-drinks", nil))
	if f.cache.Len() != 0 {
		t.Fatalf("a failed category render was cached (%d entries)", f.cache.Len())
	}
}

// Review finding 1: the inner lookups of LoadAllActive/LoadWith (barcodes,
// thumbnails, hidden flags, modifiers, variants, current prices) degrade the
// render instead of failing it. The degraded page must still be served —
// exactly as before the cache — but never stored, or e.g. a failed-open
// hidden-flag lookup or a missing price override would be served for the
// cache's whole max age after the lookup recovers.
func TestSellScreenCache_DegradedLookupRenderNotCached(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, false, false)
	f.exec(t, `DROP TABLE item_barcodes`)
	body, _ := f.list(t, h)
	if !strings.Contains(body, "Cola Can") {
		t.Fatalf("degraded render must still show the tiles:\n%s", body)
	}
	if f.cache.Len() != 0 {
		t.Fatalf("a render degraded by a failed barcode lookup was cached (%d entries)", f.cache.Len())
	}
	if _, q := f.list(t, h); q <= 1 {
		t.Fatalf("next request after a degraded render ran %d SELECTs — served the degraded page from cache", q)
	}
	if body, _ := f.category(t, h, "cat-drinks"); !strings.Contains(body, "Cola Can") {
		t.Fatalf("degraded category popup must still show the tiles:\n%s", body)
	}
	if f.cache.Len() != 0 {
		t.Fatalf("a degraded category popup was cached (%d entries)", f.cache.Len())
	}
}

// Same, for the quick-button half (LoadWith's own lookups over the
// shortcut_buttons rows).
func TestSellScreenStore_LoadWithReportsDegradedLookup(t *testing.T) {
	f := newSellScreenFixture(t)
	f.exec(t, `INSERT INTO shortcut_buttons (barcode, label, item_id, sort_order) VALUES ('COLA', 'Cola Can', 'itm-cola', 1)`)
	ctx := context.Background()
	if _, degraded, err := f.store.loadWith(ctx, nil); err != nil || degraded {
		t.Fatalf("healthy loadWith: degraded=%v err=%v, want false/nil", degraded, err)
	}
	f.exec(t, `DROP TABLE price_history`)
	btns, degraded, err := f.store.loadWith(ctx, nil)
	if err != nil || len(btns) == 0 {
		t.Fatalf("loadWith must still render around a failed price lookup: %d buttons, err=%v", len(btns), err)
	}
	if !degraded {
		t.Fatal("loadWith did not report its failed current-price lookup as degraded")
	}
	if _, degraded, err := f.store.loadAllActive(ctx); err != nil || !degraded {
		t.Fatalf("loadAllActive with a failed current-price lookup: degraded=%v err=%v, want true/nil", degraded, err)
	}
}

// Review finding 2: the next price boundary must be read BEFORE the render.
// A boundary that passes between the render's current-price read and a
// boundary read done after it is invisible to that later read (it is no
// longer in the future), so the pre-boundary render would be stored with the
// full max age. Read first, it is the entry's expiry — already passed at Put,
// so Put refuses it.
func TestSellScreenCache_PriceBoundaryPassingMidRenderNotCached(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, false, false)
	start := time.Now().UTC().Truncate(time.Second).Add(2 * time.Second)
	f.exec(t, `INSERT INTO price_history (id, item_id, price, starts_at) VALUES ('ph-mid', 'itm-cola', 77, ?)`, start.Format("2006-01-02 15:04:05"))
	rec := httptest.NewRecorder()
	h.serveSellScreen(rec, httptest.NewRequest("GET", "/ui/buttons", nil), h.sellScreenKey("list", ""), func(w http.ResponseWriter, r *http.Request) bool {
		clean := h.renderList(w, r)
		// The boundary passes after the render read its prices.
		time.Sleep(time.Until(start) + 1100*time.Millisecond)
		return clean
	})
	if strings.Contains(rec.Body.String(), httpx.FormatMoney(77, "en")) {
		t.Fatal("render ran after the boundary — the test did not exercise the race")
	}
	if f.cache.Len() != 0 {
		t.Fatalf("a pre-boundary render was cached after its price boundary passed mid-render (%d entries)", f.cache.Len())
	}
}

// ---- benchmarks ------------------------------------------------------------

// seedSellScreenBenchCatalog: ~20 categories, ~500 active items, a quick
// button on every tenth, a price override on every seventh.
func seedSellScreenBenchCatalog(b *testing.B, db *sql.DB) {
	b.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	for c := 0; c < 20; c++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO categories (id, name) VALUES (?, ?)`, fmt.Sprintf("cat-%02d", c), fmt.Sprintf("Category %02d", c)); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("itm-%03d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, category_id) VALUES (?, ?, ?, ?, ?)`,
			id, fmt.Sprintf("SKU%03d", i), fmt.Sprintf("Item %03d", i), 100+i, fmt.Sprintf("cat-%02d", i%20)); err != nil {
			b.Fatal(err)
		}
		if i%10 == 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO shortcut_buttons (barcode, label, item_id, sort_order) VALUES (?, ?, ?, ?)`,
				fmt.Sprintf("BC%03d", i), fmt.Sprintf("Item %03d", i), id, i); err != nil {
				b.Fatal(err)
			}
		}
		if i%7 == 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO price_history (id, item_id, price, starts_at) VALUES (?, ?, ?, datetime('now', '-1 day'))`,
				"ph-"+id, id, 90+i); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func benchmarkButtonsList(b *testing.B, cached bool) {
	path := testsupport.MigratedDBFile(b, "bench.db")
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	seedSellScreenBenchCatalog(b, db)
	store := NewButtonStore(db)
	var cache *SellScreenCache
	if cached {
		cache = NewSellScreenCache()
	}
	funcs := httpx.FuncsFor("en")
	// Production builds (clones) the renderer per request too
	// (internal/pages/buttons_api.go), so the benchmark does the same.
	serve := func() int {
		renderer, err := NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons.html"),
			funcs,
		)
		if err != nil {
			b.Fatal(err)
		}
		h := &ButtonsHTTP{Store: *store, View: renderer, Cache: cache, Locale: "en", BrowsingMode: browsingModeCategoryTabs}
		rec := httptest.NewRecorder()
		h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
		if rec.Code != http.StatusOK {
			b.Fatalf("List = %d", rec.Code)
		}
		return rec.Body.Len()
	}
	size := serve() // warm the template cache (and the tile cache when on)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		serve()
	}
	b.StopTimer()
	b.ReportMetric(float64(size), "body-bytes") // after ResetTimer, which clears extra metrics
}

func BenchmarkButtonsList_Uncached(b *testing.B) { benchmarkButtonsList(b, false) }
func BenchmarkButtonsList_Cached(b *testing.B)   { benchmarkButtonsList(b, true) }
