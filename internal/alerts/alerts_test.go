package alerts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
)

// The digest pushes the running-out count to the marketplace with the store
// token; nothing is sent when healthy or unregistered.
// Unusual-sales detection: yesterday vs the same weekday's 4-week average.
func TestUnusualSales(t *testing.T) {
	f := filepath.Join(t.TempDir(), "unusual.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	// ref anchors both the seeded data and every unusualSales call below to
	// ONE Go-side instant, rather than each call independently reading the
	// real clock — see unusualSales'/DayTotal's own doc comments (ut-docs#969:
	// two independent real-clock reads a moment apart can disagree about
	// what day "today" is, right around a UTC day boundary).
	ref := time.Now().UTC()
	sale := func(id string, daysAgo, total int) {
		// createdAt mirrors what production actually writes (real UTC, see
		// internal/pos/sales.go's time.Now().UTC().Format(time.RFC3339)) —
		// DayTotal's query applies a single 'localtime' conversion, which is
		// only correct if created_at is genuine UTC. Seeding local wall-clock
		// time here instead double-applies the conversion and silently shifts
		// the computed date on any non-UTC machine.
		createdAt := ref.AddDate(0, 0, -daysAgo).Format(time.RFC3339)
		mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
		          VALUES (?, ?, 'completed', 'sale', ?, 0, ?, ?)`,
			id, "R-"+id, total, total, createdAt)
	}
	// Baseline: same weekday 1-4 weeks back ≈ 1000/day.
	sale("b1", 8, 1000)
	sale("b2", 15, 900)
	sale("b3", 22, 1100)
	sale("b4", 29, 1000)

	// No yesterday sales → ratio 0 → unusual (a normally-selling day at zero).
	if ratio, _, unusual := unusualSales(context.Background(), d, ref); !unusual || ratio != 0 {
		t.Fatalf("zero day on a selling weekday should be unusual (ratio=%v unusual=%v)", ratio, unusual)
	}
	// Normal yesterday (≈ baseline) → not unusual.
	sale("y1", 1, 950)
	if ratio, _, unusual := unusualSales(context.Background(), d, ref); unusual {
		t.Fatalf("normal day flagged unusual (ratio=%v)", ratio)
	}
	// Blowout yesterday → unusual high.
	sale("y2", 1, 1500)
	if ratio, _, unusual := unusualSales(context.Background(), d, ref); !unusual || ratio < 1.8 {
		t.Fatalf("blowout not flagged (ratio=%v unusual=%v)", ratio, unusual)
	}
}

// TestUnusualSales must reach the same verdict no matter which real calendar
// day it happens to run on — the actual regression test for ut-docs#969.
// Sweeps a full week of fixed reference instants, each just after UTC
// midnight (the exact window the original bug was observed in), including
// both a Sunday and a Monday per the card's acceptance criteria.
func TestUnusualSales_EveryWeekdayIsDeterministic(t *testing.T) {
	for i := 0; i < 7; i++ {
		// 2026-08-23 is a Sunday; sweeps Sun through Sat.
		ref := time.Date(2026, 8, 23+i, 0, 0, 30, 0, time.UTC)
		t.Run(ref.Weekday().String(), func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "unusual_weekday.db")
			database, err := db.Open(f)
			if err != nil {
				t.Fatalf("open db: %v", err)
			}
			defer database.Close()
			d := database.DB
			mustExec := func(q string, args ...any) {
				t.Helper()
				if _, err := d.Exec(q, args...); err != nil {
					t.Fatalf("exec: %v", err)
				}
			}
			sale := func(id string, daysAgo, total int) {
				createdAt := ref.AddDate(0, 0, -daysAgo).Format(time.RFC3339)
				mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
				          VALUES (?, ?, 'completed', 'sale', ?, 0, ?, ?)`,
					id, "R-"+id, total, total, createdAt)
			}
			sale("b1", 8, 1000)
			sale("b2", 15, 900)
			sale("b3", 22, 1100)
			sale("b4", 29, 1000)

			if ratio, _, unusual := unusualSales(context.Background(), d, ref); !unusual || ratio != 0 {
				t.Fatalf("zero day on a selling weekday should be unusual (ratio=%v unusual=%v)", ratio, unusual)
			}
			sale("y1", 1, 950)
			if ratio, _, unusual := unusualSales(context.Background(), d, ref); unusual {
				t.Fatalf("normal day flagged unusual (ratio=%v)", ratio)
			}
			sale("y2", 1, 1500)
			if ratio, _, unusual := unusualSales(context.Background(), d, ref); !unusual || ratio < 1.8 {
				t.Fatalf("blowout not flagged (ratio=%v unusual=%v)", ratio, unusual)
			}
		})
	}
}

func TestPushDigest(t *testing.T) {
	f := filepath.Join(t.TempDir(), "alerts.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	// Fast seller with 3 days of stock (the seeded demo catalog has no sales).
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-x','Cola','X',100,1)`)
	mustExec(`INSERT INTO stock_locations (id, name) VALUES ('loc-x','Floor')`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-x','it-x','loc-x',6)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
	          VALUES ('sx','RX','completed','sale',5600,0,5600, datetime('now','-1 days'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	          VALUES ('lx','sx',1,'it-x','Cola',56,100,0,0,0,5600,5600)`)

	var got map[string]any
	var auth string
	mp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stores/notify" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ok": true}})
	}))
	defer mp.Close()

	// Unregistered → no push, no error.
	if err := pushDigest(context.Background(), &config.Config{}, d); err != nil {
		t.Fatalf("unregistered: %v", err)
	}
	if got != nil {
		t.Fatal("unregistered till must not push")
	}

	// The till's install-time default locale (ut-docs#658) rides along on
	// the push, verbatim as DefaultLocale() reports it — a BCP-47 tag
	// ("de-DE"), same shape UT_DEFAULT_LOCALE actually takes
	// (internal/config), not a bare code. Normalizing that shape into
	// mailText's bare-code keys is the marketplace's job (ut-cloud's own
	// resolveLocale test), not this repo's — this test only proves the
	// till sends what it's actually configured with, unmassaged.
	httpx.InitI18n(nil, "de-DE")
	t.Cleanup(func() { httpx.InitI18n(nil, "en") })

	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: mp.URL + "/api", StoreID: "store-abc", MerchantToken: "tok-1",
	}}
	if err := pushDigest(context.Background(), cfg, d); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got == nil || got["type"] != "low_stock_digest" || got["store_id"] != "store-abc" {
		t.Fatalf("pushed = %+v", got)
	}
	if got["locale"] != "de-DE" {
		t.Fatalf("locale = %v, want de-DE", got["locale"])
	}
	payload, _ := got["payload"].(map[string]any)
	if payload["running_out"] != float64(1) {
		t.Fatalf("running_out = %v, want 1", payload["running_out"])
	}
	if auth != "Bearer tok-1" {
		t.Fatalf("auth = %q", auth)
	}
}

// Start's digest loop must be genuinely joinable: wg.Wait() must NOT return
// while the loop is still running (a missing wg.Add would let Wait return
// immediately, vacuously "passing" without ever tracking the goroutine), and
// MUST return promptly once ctx is cancelled (a missing wg.Done would hang it
// forever) — this is what lets app.Run safely close the DB right after Wait
// returns. The loop's first action is a 2-minute wait-or-cancel select, so
// cancelling well inside that window proves the goroutine is genuinely
// parked there, not already finished on its own.
func TestStart_JoinsOnCancel(t *testing.T) {
	f := filepath.Join(t.TempDir(), "join.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	Start(ctx, &config.Config{}, database.DB, &wg)

	if waitWithin(&wg, 150*time.Millisecond) {
		t.Fatal("wg.Wait() returned before ctx was even cancelled — digest goroutine not tracked")
	}
	cancel()
	if !waitWithin(&wg, 2*time.Second) {
		t.Fatal("Start's digest goroutine did not join wg within 2s of ctx cancel")
	}
}

// waitWithin reports whether wg.Wait() returns within d.
func waitWithin(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// runningOutCount propagates real query failures rather than swallowing them
// — a no-sales till reports 0 with no error (the len(rates)==0 branch), but
// a genuine query failure on either underlying table must surface as an
// error so pushDigest's caller can log and retry, not silently report "0
// running low."
func TestRunningOutCount_NoSalesIsNotAnError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "empty.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	n, err := runningOutCount(context.Background(), database.DB)
	if err != nil || n != 0 {
		t.Fatalf("runningOutCount on an empty till = (%d, %v), want (0, nil)", n, err)
	}
}

func TestRunningOutCount_SellRatesQueryError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "brokensales.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if _, err := database.DB.Exec(`DROP TABLE sale_lines`); err != nil {
		t.Fatalf("drop sale_lines: %v", err)
	}

	if _, err := runningOutCount(context.Background(), database.DB); err == nil {
		t.Fatal("want error when the sell-rates query's own table is gone")
	}
}

func TestRunningOutCount_StockLevelsQueryError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "brokeninv.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	// A real completed sale so ItemDailySellRates returns a non-empty map —
	// otherwise runningOutCount short-circuits before ever reaching
	// ListStockLevels, and the table drop below would go untested.
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-y','Widget','Y',100,1)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
	          VALUES ('sy','RY','completed','sale',1000,0,1000, datetime('now'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	          VALUES ('ly','sy',1,'it-y','Widget',10,100,0,0,0,1000,1000)`)

	if _, err := d.Exec(`DROP TABLE inventory`); err != nil {
		t.Fatalf("drop inventory: %v", err)
	}

	if _, err := runningOutCount(context.Background(), d); err == nil {
		t.Fatal("want error when the stock-levels query's own table is gone despite valid sell rates")
	}
}

func TestPushDigest_PropagatesRunningOutCountError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "digesterr.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if _, err := database.DB.Exec(`DROP TABLE sale_lines`); err != nil {
		t.Fatalf("drop sale_lines: %v", err)
	}
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: "http://unused.invalid", StoreID: "store-x", MerchantToken: "tok",
	}}
	if err := pushDigest(context.Background(), cfg, database.DB); err == nil {
		t.Fatal("want pushDigest to propagate the underlying query error")
	}
}

func TestPushDigest_NothingRunningOutIsNotPushed(t *testing.T) {
	f := filepath.Join(t.TempDir(), "healthy.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	var hit bool
	mp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer mp.Close()
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: mp.URL, StoreID: "store-x", MerchantToken: "tok",
	}}
	if err := pushDigest(context.Background(), cfg, database.DB); err != nil {
		t.Fatalf("healthy till: %v", err)
	}
	if hit {
		t.Fatal("a till with nothing running out must not push")
	}
}

// runningOutCount's own comment says it "mirrors the inventory page's
// model" (internal/pages/inventory_page.go's stockLevelsForDisplay) — once
// that model became lead-time-aware (universaltill/ut-docs#85), this must
// stay in sync or the daily digest silently under-counts items the
// inventory page is already warning about.
func TestRunningOutCount_LeadTimeAware(t *testing.T) {
	f := filepath.Join(t.TempDir(), "leadtime-digest.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v (%s)", err, q)
		}
	}
	// Same fixture shape as inventory_prediction_test.go's
	// TestInventoryLeadTimeAwareWarnAndReorder: rate 2/day, 16 on hand →
	// DaysLeft=8, a 10-day lead time. The flat "<=7" check would miss this
	// (8 > 7); the inventory page's own effective-warn-days logic warns
	// on it (8 <= 10).
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active, lead_time_days) VALUES ('it-slow','Slow Ship','SHIP',100,1,10)`)
	mustExec(`INSERT INTO stock_locations (id, name) VALUES ('loc-1','Shop floor')`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-1','it-slow','loc-1',16)`)
	for i := 0; i < 14; i++ {
		saleID := "s-" + string(rune('a'+i))
		mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
		          VALUES (?, ?, 'completed', 'sale', 400, 0, 400, datetime('now', ?))`,
			saleID, "R-"+saleID, "-"+string(rune('0'+i%9))+" days")
		mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		          VALUES (?, ?, 1, 'it-slow', 'Slow Ship', 4, 100, 0, 0, 0, 400, 400)`, "l-"+saleID, saleID)
	}

	n, err := runningOutCount(context.Background(), d)
	if err != nil {
		t.Fatalf("runningOutCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("runningOutCount = %d, want 1 (the item's own 10-day lead time makes DaysLeft=8 count as running out, same as /inventory)", n)
	}
}

func TestPushDigest_PropagatesPushNotifyError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "notifyerr.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-z','Cola','Z',100,1)`)
	mustExec(`INSERT INTO stock_locations (id, name) VALUES ('loc-z','Floor')`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-z','it-z','loc-z',6)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
	          VALUES ('sz','RZ','completed','sale',5600,0,5600, datetime('now','-1 days'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	          VALUES ('lz','sz',1,'it-z','Cola',56,100,0,0,0,5600,5600)`)

	mp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mp.Close()
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: mp.URL, StoreID: "store-z", MerchantToken: "tok",
	}}
	if err := pushDigest(context.Background(), cfg, d); err == nil {
		t.Fatal("want pushDigest to propagate pushNotify's error")
	}
}

// pushNotify's own guard (called directly outside pushDigest's short-circuit,
// e.g. from Start's unusual-sales push) must also no-op cleanly when the
// till isn't registered.
func TestPushNotify_UnregisteredIsNoop(t *testing.T) {
	if err := pushNotify(context.Background(), &config.Config{}, "unusual_sales", nil); err != nil {
		t.Fatalf("unregistered pushNotify: %v", err)
	}
}

func TestPushNotify_MalformedEndpointURL(t *testing.T) {
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: "http://example.com\n", StoreID: "store-x", MerchantToken: "tok",
	}}
	if err := pushNotify(context.Background(), cfg, "t", nil); err == nil {
		t.Fatal("want a request-construction error for a control-character URL")
	}
}

func TestPushNotify_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // connection refused from here on
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: srv.URL, StoreID: "store-x", MerchantToken: "tok",
	}}
	if err := pushNotify(context.Background(), cfg, "t", nil); err == nil {
		t.Fatal("want a transport error when the marketplace is unreachable")
	}
}

// unusualSales needs at least 3 of the 4 baseline weeks to have sold
// something before it will call anything unusual — with only 2, it must
// stay silent rather than compare against a thin baseline.
func TestUnusualSales_ThinBaselineIsNotUnusual(t *testing.T) {
	f := filepath.Join(t.TempDir(), "thin.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	ref := time.Now().UTC()
	sale := func(id string, daysAgo, total int) {
		createdAt := ref.AddDate(0, 0, -daysAgo).Format(time.RFC3339)
		mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
		          VALUES (?, ?, 'completed', 'sale', ?, 0, ?, ?)`,
			id, "R-"+id, total, total, createdAt)
	}
	// Only 2 of the 4 baseline weeks sold anything; a huge "yesterday" must
	// still not be flagged since the baseline itself is too thin to trust.
	sale("b1", 8, 1000)
	sale("b2", 15, 1000)
	sale("y", 1, 5000)

	if ratio, _, unusual := unusualSales(context.Background(), d, ref); unusual {
		t.Fatalf("thin baseline (2/4 weeks) must not be flagged unusual (ratio=%v)", ratio)
	}
}

// Start's actual loop body (not just its cancel-before-first-fire join) must
// run pushDigest/unusualSales and tick again — proven here via the
// package's own test-overridable firstDelay/tickInterval, driven fast enough
// to observe at least one real digest push land on a fake marketplace.
func TestStart_RunsDigestLoopBody(t *testing.T) {
	origFirst, origTick := firstDelayNS.Load(), tickIntervalNS.Load()
	t.Cleanup(func() { firstDelayNS.Store(origFirst); tickIntervalNS.Store(origTick) })
	firstDelayNS.Store(int64(2 * time.Millisecond))
	tickIntervalNS.Store(int64(2 * time.Millisecond))

	f := filepath.Join(t.TempDir(), "loop.db")
	database, err := db.Open(f)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	d := database.DB
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	mustExec(`INSERT INTO items (id, name, sku, base_price, is_active) VALUES ('it-l','Cola','L',100,1)`)
	mustExec(`INSERT INTO stock_locations (id, name) VALUES ('loc-l','Floor')`)
	mustExec(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-l','it-l','loc-l',6)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
	          VALUES ('sl','RL','completed','sale',5600,0,5600, datetime('now','-1 days'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	          VALUES ('ll','sl',1,'it-l','Cola',56,100,0,0,0,5600,5600)`)

	// Also seed an unusual-sales baseline (4 selling weeks back) plus a
	// blowout "yesterday" so Start's loop exercises BOTH of its pushes in
	// one iteration — a modest low-stock digest is not enough on its own
	// to prove the unusual-sales half of the loop body actually runs.
	noon := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	sale := func(id string, daysAgo, total int) {
		createdAt := noon.AddDate(0, 0, -daysAgo).Format(time.RFC3339)
		mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, tax_total, total, created_at)
		          VALUES (?, ?, 'completed', 'sale', ?, 0, ?, ?)`,
			id, "R-"+id, total, total, createdAt)
	}
	sale("bl1", 8, 1000)
	sale("bl2", 15, 1000)
	sale("bl3", 22, 1000)
	sale("bl4", 29, 1000)
	sale("yl", 1, 5000) // ratio ≈5.0 vs baseline ≈1000 → unusual

	var digestHits, unusualHits int32
	mp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["type"] {
		case "low_stock_digest":
			atomic.AddInt32(&digestHits, 1)
		case "unusual_sales":
			atomic.AddInt32(&unusualHits, 1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ok": true}})
	}))
	defer mp.Close()
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: mp.URL, StoreID: "store-l", MerchantToken: "tok-l",
	}}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	Start(ctx, cfg, d, &wg)

	deadline := time.Now().Add(2 * time.Second)
	for (atomic.LoadInt32(&digestHits) == 0 || atomic.LoadInt32(&unusualHits) == 0) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&digestHits) == 0 {
		t.Fatal("Start's loop never pushed a low-stock digest within 2s of fast-forwarded timers")
	}
	if atomic.LoadInt32(&unusualHits) == 0 {
		t.Fatal("Start's loop never pushed an unusual-sales notification within 2s of fast-forwarded timers")
	}
	cancel()
	if !waitWithin(&wg, 2*time.Second) {
		t.Fatal("Start's loop goroutine did not join wg within 2s of ctx cancel")
	}
}
