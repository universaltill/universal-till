package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2535: till-side producer for the cloud sales-aggregate endpoint
// (ADR-0111). These drive pushSalesAggregates / Tick against an
// httptest cloud and assert what goes over the wire.

// aggCloud records every POST to /v1/stores/sales-aggregates (raw body,
// auth header, path) and answers with a settable status.
type aggCloud struct {
	mu     sync.Mutex
	status int
	posts  []aggPost
}

type aggPost struct {
	path, auth string
	raw        []byte
	body       map[string]any
}

func (c *aggCloud) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		c.mu.Lock()
		c.posts = append(c.posts, aggPost{path: r.URL.Path, auth: r.Header.Get("Authorization"), raw: raw, body: body})
		status := c.status
		c.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status == http.StatusPaymentRequired {
			_, _ = w.Write([]byte(`{"data":null,"error":{"code":"subscription_inactive","message":"x"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true},"error":null}`))
	})
}

func (c *aggCloud) setStatus(s int) { c.mu.Lock(); c.status = s; c.mu.Unlock() }
func (c *aggCloud) count() int      { c.mu.Lock(); defer c.mu.Unlock(); return len(c.posts) }
func (c *aggCloud) since(n int) []aggPost {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]aggPost(nil), c.posts[n:]...)
}

// noAggThrottle disables the 10-minute throttle for one test.
func noAggThrottle(t *testing.T) {
	t.Helper()
	prevInterval := salesAggregateIntervalNS.Load()
	salesAggregateIntervalNS.Store(0)
	salesAggregateLastNS.Store(0)
	t.Cleanup(func() {
		salesAggregateIntervalNS.Store(prevInterval)
		salesAggregateLastNS.Store(0)
	})
}

func aggExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func aggAt(tm time.Time) string { return tm.UTC().Format("2006-01-02T15:04:05Z") }

func aggDay(t *testing.T, db *sql.DB, tm time.Time) string {
	t.Helper()
	var d string
	if err := db.QueryRow(`SELECT date(?, 'localtime')`, aggAt(tm)).Scan(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

// aggSeedSale inserts one completed sale on register reg-A rung by u1: one
// line of item-1 at 19% (net = total − tax), paid in cash.
func aggSeedSale(t *testing.T, db *sql.DB, id string, at time.Time, tax, total int64) {
	t.Helper()
	ts := aggAt(at)
	aggExec(t, db, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, local_date, register_id, cashier_id)
VALUES (?, ?, 'completed', 'sale', 'GBP', ?, 0, ?, ?, ?, date(?, 'localtime'), 'reg-A', 'u1')`, id, "R-"+id, total-tax, tax, total, ts, ts)
	aggExec(t, db, `INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES (?, ?, 1, 'item-1', 'Item 1', 1, ?, 0, 1900, ?, ?, ?)`, id+"-l1", id, total, tax, total-tax, total)
	aggExec(t, db, `INSERT INTO payments (id, sale_id, method_id, amount, currency, change_given, tip_amount) VALUES (?, ?, 'cash', ?, 'GBP', 0, 0)`, "p-"+id, id, total)
}

type aggFixture struct {
	db               *sql.DB
	today, prevDay   string
	todayT, prevDayT time.Time
}

func seedAggFixture(t *testing.T, name string) aggFixture {
	t.Helper()
	d := openMigratedDB(t, name)
	aggExec(t, d.DB, `INSERT INTO registers (id, name) VALUES ('reg-A', 'Front')`)
	aggExec(t, d.DB, `INSERT INTO users (id, username, display_name) VALUES ('u1', 'alice', 'Alice Example')`)
	aggExec(t, d.DB, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('item-1', 'SKU-1', 'Item 1', 1190, 1)`)
	now := time.Now()
	todayT := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	prevT := todayT.AddDate(0, 0, -1)
	aggSeedSale(t, d.DB, "s-today", todayT, 190, 1190)
	aggSeedSale(t, d.DB, "s-prev", prevT, 190, 1190)
	return aggFixture{db: d.DB, todayT: todayT, prevDayT: prevT, today: aggDay(t, d.DB, todayT), prevDay: aggDay(t, d.DB, prevT)}
}

func TestPushSalesAggregates_PostsOneRollupPerDayTill(t *testing.T) {
	noAggThrottle(t)
	cloud := &aggCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	f := seedAggFixture(t, "agg-post.db")

	pushSalesAggregates(context.Background(), testCfg(srv.URL), f.db)

	posts := cloud.since(0)
	if len(posts) != 2 {
		t.Fatalf("posts = %d, want 2 (one per business date)", len(posts))
	}
	byDate := map[string]aggPost{}
	for _, p := range posts {
		if p.path != "/v1/stores/sales-aggregates" {
			t.Fatalf("path = %q", p.path)
		}
		if p.auth != "Bearer tok-1" {
			t.Fatalf("auth = %q", p.auth)
		}
		byDate[p.body["business_date"].(string)] = p
	}
	p, ok := byDate[f.today]
	if !ok {
		t.Fatalf("no rollup for today %s: %v", f.today, byDate)
	}
	b := p.body
	if b["store_id"] != "store-1" || b["till_id"] != "reg-A" {
		t.Fatalf("store/till = %v/%v", b["store_id"], b["till_id"])
	}
	// Every breakdown is an array, never null — by_cashier included while
	// the staff toggle is off.
	for _, k := range []string{"hourly", "by_cashier", "by_item_category", "by_payment_method", "by_vat_rate"} {
		if _, isArr := b[k].([]any); !isArr {
			t.Fatalf("%s = %#v, want a JSON array", k, b[k])
		}
	}
	if !strings.Contains(string(p.raw), `"by_cashier":[]`) {
		t.Fatalf("by_cashier must be [] with the toggle off: %s", p.raw)
	}
	hourly := b["hourly"].([]any)
	if len(hourly) != 1 {
		t.Fatalf("hourly = %v", hourly)
	}
	h := hourly[0].(map[string]any)
	if h["net_sales_minor"] != float64(1190) || h["sales_count"] != float64(1) {
		t.Fatalf("hour bucket = %v", h)
	}
	item := b["by_item_category"].([]any)[0].(map[string]any)
	if item["item_or_category_id"] != "item-1" || item["qty"] != float64(1) || item["share_basis_points"] != float64(10000) || item["top_modifier_id"] != "" {
		t.Fatalf("item bucket = %v", item)
	}
	pay := b["by_payment_method"].([]any)[0].(map[string]any)
	if pay["method"] != "cash" || pay["amount_minor"] != float64(1190) || pay["tips_minor"] != float64(0) || pay["expected_cash_minor"] != float64(1190) {
		t.Fatalf("payment bucket = %v", pay)
	}
	vat := b["by_vat_rate"].([]any)
	if len(vat) != 1 {
		t.Fatalf("vat = %v", vat)
	}
	v := vat[0].(map[string]any)
	if v["rate_basis_points"] != float64(1900) || v["net_minor"] != float64(1000) || v["vat_minor"] != float64(190) {
		t.Fatalf("vat bucket = %v", v)
	}
	for _, k := range []string{"refund_count", "void_count", "no_sale_count", "discount_count"} {
		if b[k] != float64(0) {
			t.Fatalf("%s = %v, want 0", k, b[k])
		}
	}
}

func TestPushSalesAggregates_CashierBreakdownFollowsSetting(t *testing.T) {
	noAggThrottle(t)
	cloud := &aggCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	f := seedAggFixture(t, "agg-cashier.db")
	ctx := context.Background()
	if err := data.NewSettingsRepo(f.db).Set(ctx, "reports.cloud_staff_breakdown", "true"); err != nil {
		t.Fatal(err)
	}

	pushSalesAggregates(ctx, testCfg(srv.URL), f.db)

	posts := cloud.since(0)
	if len(posts) != 2 {
		t.Fatalf("posts = %d, want 2", len(posts))
	}
	for _, p := range posts {
		if strings.Contains(string(p.raw), "Alice") || strings.Contains(string(p.raw), "alice") {
			t.Fatalf("a staff NAME leaked into the upload: %s", p.raw)
		}
		cs := p.body["by_cashier"].([]any)
		if len(cs) != 1 {
			t.Fatalf("by_cashier = %v", cs)
		}
		c := cs[0].(map[string]any)
		if c["staff_id"] != "u1" || c["net_sales_minor"] != float64(1190) || c["sales_count"] != float64(1) ||
			c["avg_sale_minor"] != float64(1190) || c["items_per_sale"] != float64(1) ||
			c["no_sale_opens"] != float64(0) || c["hours_on_till"] != float64(0) {
			t.Fatalf("cashier bucket = %v", c)
		}
	}
}

func TestPushSalesAggregates_HashGateResendsOnlyChangedDay(t *testing.T) {
	noAggThrottle(t)
	cloud := &aggCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	f := seedAggFixture(t, "agg-hash.db")
	ctx := context.Background()
	cfg := testCfg(srv.URL)

	pushSalesAggregates(ctx, cfg, f.db)
	if cloud.count() != 2 {
		t.Fatalf("first push = %d posts, want 2", cloud.count())
	}
	pushSalesAggregates(ctx, cfg, f.db)
	if cloud.count() != 2 {
		t.Fatalf("unchanged data re-sent: %d posts", cloud.count())
	}

	// A late sale on yesterday changes that day only.
	aggSeedSale(t, f.db, "s-prev-2", f.prevDayT.Add(time.Hour), 95, 595)
	n := cloud.count()
	pushSalesAggregates(ctx, cfg, f.db)
	re := cloud.since(n)
	if len(re) != 1 || re[0].body["business_date"] != f.prevDay {
		t.Fatalf("resend = %d posts (%v), want exactly yesterday %s", len(re), re, f.prevDay)
	}
}

func TestPushSalesAggregates_FailuresRecordNothingAndRetry(t *testing.T) {
	noAggThrottle(t)
	cloud := &aggCloud{status: http.StatusPaymentRequired}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	f := seedAggFixture(t, "agg-fail.db")
	ctx := context.Background()
	cfg := testCfg(srv.URL)
	repo := data.NewPOSRepo(f.db)

	pushSalesAggregates(ctx, cfg, f.db)
	if cloud.count() != 1 {
		t.Fatalf("402 must stop the round after the first refusal: %d posts", cloud.count())
	}
	for _, day := range []string{f.today, f.prevDay} {
		if _, ok, _ := repo.SalesAggregateUploadHash(ctx, day, "reg-A"); ok {
			t.Fatalf("402 recorded an upload for %s", day)
		}
	}

	cloud.setStatus(http.StatusInternalServerError)
	pushSalesAggregates(ctx, cfg, f.db)
	if cloud.count() != 2 {
		t.Fatalf("500 must stop the round after one attempt: %d posts", cloud.count())
	}
	for _, day := range []string{f.today, f.prevDay} {
		if _, ok, _ := repo.SalesAggregateUploadHash(ctx, day, "reg-A"); ok {
			t.Fatalf("500 recorded an upload for %s", day)
		}
	}

	cloud.setStatus(http.StatusOK)
	pushSalesAggregates(ctx, cfg, f.db)
	if got := len(cloud.since(2)); got != 2 {
		t.Fatalf("retry after failures sent %d, want both days", got)
	}
	for _, day := range []string{f.today, f.prevDay} {
		if _, ok, _ := repo.SalesAggregateUploadHash(ctx, day, "reg-A"); !ok {
			t.Fatalf("200 did not record the upload for %s", day)
		}
	}
}

// A rollup the cloud rejects as malformed (a non-retryable 4xx) must not
// wedge the queue: every newer day would otherwise stay unsent until the
// rejected one aged out of the lookback window.
func TestPushSalesAggregates_RejectedDaySkippedNotBlocking(t *testing.T) {
	noAggThrottle(t)
	cloud := &aggCloud{status: http.StatusBadRequest}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	f := seedAggFixture(t, "agg-reject.db")
	ctx := context.Background()
	cfg := testCfg(srv.URL)
	repo := data.NewPOSRepo(f.db)

	pushSalesAggregates(ctx, cfg, f.db)
	if cloud.count() != 2 {
		t.Fatalf("a 400 must skip that day and try the next: %d posts, want 2", cloud.count())
	}
	for _, day := range []string{f.today, f.prevDay} {
		if _, ok, _ := repo.SalesAggregateUploadHash(ctx, day, "reg-A"); ok {
			t.Fatalf("400 recorded an upload for %s", day)
		}
	}
}

func TestPushSalesAggregates_Throttled(t *testing.T) {
	noAggThrottle(t)
	salesAggregateIntervalNS.Store(int64(10 * time.Minute))
	cloud := &aggCloud{}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	f := seedAggFixture(t, "agg-throttle.db")
	ctx := context.Background()
	cfg := testCfg(srv.URL)

	pushSalesAggregates(ctx, cfg, f.db)
	if cloud.count() != 2 {
		t.Fatalf("first push = %d", cloud.count())
	}
	aggSeedSale(t, f.db, "s-today-2", f.todayT.Add(time.Hour), 95, 595)
	pushSalesAggregates(ctx, cfg, f.db)
	if cloud.count() != 2 {
		t.Fatalf("a push inside the throttle window sent %d", cloud.count()-2)
	}
}

// Tick gates: the rollup rides the same primary-only, registered-only slot
// as the catalog snapshot.
func TestTickSalesAggregates_RegisteredPrimaryOnly(t *testing.T) {
	t.Run("pushes on a registered primary", func(t *testing.T) {
		noAggThrottle(t)
		cloud := &fakeCloud{}
		srv := httptest.NewServer(cloud.handler())
		defer srv.Close()
		f := seedAggFixture(t, "agg-tick-primary.db")
		if err := Tick(context.Background(), testCfg(srv.URL), f.db, Hooks{}); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if len(cloud.aggregates) != 2 {
			t.Fatalf("aggregates = %d, want 2", len(cloud.aggregates))
		}
	})
	t.Run("unregistered sends nothing", func(t *testing.T) {
		noAggThrottle(t)
		cloud := &aggCloud{}
		srv := httptest.NewServer(cloud.handler())
		defer srv.Close()
		f := seedAggFixture(t, "agg-tick-unreg.db")
		cfg := testCfg(srv.URL)
		cfg.Marketplace.StoreID, cfg.Marketplace.MerchantToken = "", ""
		if err := Tick(context.Background(), cfg, f.db, Hooks{}); err != nil {
			t.Fatalf("tick: %v", err)
		}
		for _, p := range cloud.since(0) {
			if p.path == "/v1/stores/sales-aggregates" {
				t.Fatalf("unregistered till uploaded a sales aggregate")
			}
		}
	})
	t.Run("replica sends nothing", func(t *testing.T) {
		noAggThrottle(t)
		cloud := &fakeCloud{}
		srv := httptest.NewServer(cloud.handler())
		defer srv.Close()
		f := seedAggFixture(t, "agg-tick-replica.db")
		if err := data.NewSettingsRepo(f.db).Set(context.Background(), "sync.primary_url", "http://10.0.0.2:8080"); err != nil {
			t.Fatal(err)
		}
		if err := Tick(context.Background(), testCfg(srv.URL), f.db, Hooks{}); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if len(cloud.aggregates) != 0 {
			t.Fatalf("replica uploaded %d aggregates", len(cloud.aggregates))
		}
	})
}
