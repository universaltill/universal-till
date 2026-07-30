package alerts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
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
	// noon anchors seeded sales to midday UTC rather than time.Now()'s clock
	// time — DayTotal's 'localtime' bucketing means a midnight-adjacent clock
	// time could still land on the wrong side of a local calendar-day
	// boundary in a DST-observing zone right after a transition; midday UTC
	// keeps every plausible UTC offset on the same local day.
	noon := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	sale := func(id string, daysAgo, total int) {
		// createdAt mirrors what production actually writes (real UTC, see
		// internal/pos/sales.go's time.Now().UTC().Format(time.RFC3339)) —
		// DayTotal's query applies a single 'localtime' conversion, which is
		// only correct if created_at is genuine UTC. Seeding local wall-clock
		// time here instead double-applies the conversion and silently shifts
		// the computed date on any non-UTC machine.
		createdAt := noon.AddDate(0, 0, -daysAgo).Format(time.RFC3339)
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
	if ratio, _, unusual := unusualSales(context.Background(), d); !unusual || ratio != 0 {
		t.Fatalf("zero day on a selling weekday should be unusual (ratio=%v unusual=%v)", ratio, unusual)
	}
	// Normal yesterday (≈ baseline) → not unusual.
	sale("y1", 1, 950)
	if ratio, _, unusual := unusualSales(context.Background(), d); unusual {
		t.Fatalf("normal day flagged unusual (ratio=%v)", ratio)
	}
	// Blowout yesterday → unusual high.
	sale("y2", 1, 1500)
	if ratio, _, unusual := unusualSales(context.Background(), d); !unusual || ratio < 1.8 {
		t.Fatalf("blowout not flagged (ratio=%v unusual=%v)", ratio, unusual)
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

	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: mp.URL + "/api", StoreID: "store-abc", MerchantToken: "tok-1",
	}}
	if err := pushDigest(context.Background(), cfg, d); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got == nil || got["type"] != "low_stock_digest" || got["store_id"] != "store-abc" {
		t.Fatalf("pushed = %+v", got)
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
