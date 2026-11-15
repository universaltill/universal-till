package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/pos"
)

// ADR-0169 §8 (ut-docs#4763): the main till of a cloud-master store uploads
// its changed on-hand rows — at the end of a check-in that reached the cloud
// (any tier) and, on the realtime tier, from a loop at most every 5 s. A
// replica, a till-master store and an adopting store never do; a failed
// upload never fails the check-in or a sale.

const (
	stockLocalItem = "stock-item-1"
	stockCloudItem = "55555555-5555-4555-8555-555555555555"
)

// stockFeed is the fake POST /v1/stores/stock/levels.
type stockFeed struct {
	mu     sync.Mutex
	bodies []map[string]any
	status int // 0 = 200 "applied" for every row
	code   string
	// older answers every row stale/older_stream (a cloud that holds a newer
	// stream and keeps doing so).
	older bool
	hits  int
}

func (f *stockFeed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(b, &body)
	f.mu.Lock()
	status, code := f.status, f.code
	f.hits++
	older := f.older
	if r.Header.Get("Authorization") == "Bearer tok-1" && status == 0 {
		f.bodies = append(f.bodies, body)
	}
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok-1" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if status != 0 {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": map[string]any{"code": code, "message": "x"}})
		return
	}
	var results []map[string]any
	rows, _ := body["rows"].([]any)
	for _, r := range rows {
		row := map[string]any{"seq": r.(map[string]any)["seq"], "status": "applied"}
		if older {
			row["status"], row["code"] = "stale", "older_stream"
		}
		results = append(results, row)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"results": results}, "error": nil})
}

func (f *stockFeed) set(status int, code string) {
	f.mu.Lock()
	f.status, f.code = status, code
	f.mu.Unlock()
}

func (f *stockFeed) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.bodies...)
}

// seedStock gives the migrated DB one linked item with 8.5 units on hand.
func seedStock(db *sql.DB) {
	for _, q := range []string{
		`INSERT INTO items (id, name, base_price) VALUES ('` + stockLocalItem + `', 'Tea', 250)`,
		`INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at) VALUES ('item', '` + stockCloudItem + `', '` + stockLocalItem + `', 1, 'x')`,
		`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-1', '` + stockLocalItem + `', 'loc_main', 8.5)`,
		`INSERT INTO registers (id, name, location_id) VALUES ('reg-1', 'Till 1', 'loc_main')`,
		`INSERT INTO users (id, username, display_name, role) VALUES ('cashier-1', 'cashier1', 'Cashier', 'cashier')`,
	} {
		if _, err := db.Exec(q); err != nil {
			panic(err)
		}
	}
}

// stockServer mounts the fake cloud (sync, an empty catalogue feed) and the
// stock endpoint.
func stockServer(t *testing.T, master string, stock *stockFeed) string {
	t.Helper()
	cloud := &fakeCloud{entitlement: json.RawMessage(`{"plan":"shop","subscription_status":"active","catalog_master":"` + master + `"}`)}
	mux := http.NewServeMux()
	mux.Handle("/v1/stores/stock/levels", stock)
	mux.HandleFunc("/v1/stores/catalog", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"rows": []any{}, "next": 0, "high_water": 0}, "error": nil})
	})
	mux.Handle("/", cloud.handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func stockMarks(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM stock_upload_outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTickUploadsOnHandOnACloudMasterMainTill(t *testing.T) {
	d := openMigratedDB(t, "stockup.db")
	seedStock(d.DB)
	stock := &stockFeed{}
	url := stockServer(t, "cloud", stock)
	if err := Tick(context.Background(), testCfg(url), d.DB, Hooks{}); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	reqs := stock.requests()
	if len(reqs) != 1 {
		t.Fatalf("stock requests = %d, want 1", len(reqs))
	}
	if reqs[0]["store_id"] != "store-1" || reqs[0]["stream_id"] == "" || reqs[0]["till_version"] == nil {
		t.Fatalf("request = %v", reqs[0])
	}
	row := reqs[0]["rows"].([]any)[0].(map[string]any)
	if row["item_id"] != stockCloudItem || row["on_hand_milli"] != float64(8500) || row["location_id"] != "loc_main" {
		t.Fatalf("row = %v", row)
	}
	if stockMarks(t, d.DB) != 0 {
		t.Fatal("the accepted upload left its mark")
	}
	// Nothing changed: the next check-in sends nothing.
	if err := Tick(context.Background(), testCfg(url), d.DB, Hooks{}); err != nil {
		t.Fatal(err)
	}
	if len(stock.requests()) != 1 {
		t.Fatal("an unchanged stock was uploaded again")
	}
}

func TestTickNeverUploadsStockOffTheMainTillOfACloudMasterStore(t *testing.T) {
	for name, tc := range map[string]struct{ master, primary string }{
		"till master":  {"till", ""},
		"adopting":     {"adopting", ""},
		"unknown mode": {"x", ""},
		"replica":      {"cloud", "https://main-till.local:8443"},
	} {
		t.Run(name, func(t *testing.T) {
			d := openMigratedDB(t, "stocknone.db")
			seedStock(d.DB)
			if tc.primary != "" {
				if err := data.NewSettingsRepo(d.DB).Set(context.Background(), "sync.primary_url", tc.primary); err != nil {
					t.Fatal(err)
				}
			}
			stock := &stockFeed{}
			url := stockServer(t, tc.master, stock)
			if err := Tick(context.Background(), testCfg(url), d.DB, Hooks{}); err != nil {
				t.Fatalf("Tick: %v", err)
			}
			if n := len(stock.requests()); n != 0 {
				t.Fatalf("%s uploaded stock (%d requests)", name, n)
			}
			if stockMarks(t, d.DB) != 1 {
				t.Fatalf("marks = %d: they must wait, untouched", stockMarks(t, d.DB))
			}
		})
	}
}

func TestTickStockRefusalNeverFailsTheCheckinAndKeepsTheMarks(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{503, "stock_unavailable"}, {409, "till_too_old"}, {403, "not_main_till"}, {404, ""}, {401, "unauthorized"}} {
		d := openMigratedDB(t, "stockfail.db")
		seedStock(d.DB)
		stock := &stockFeed{}
		stock.set(tc.status, tc.code)
		url := stockServer(t, "cloud", stock)
		if err := Tick(context.Background(), testCfg(url), d.DB, Hooks{}); err != nil {
			t.Fatalf("%d: a refused stock upload failed the check-in: %v", tc.status, err)
		}
		if stockMarks(t, d.DB) != 1 {
			t.Fatalf("%d: marks = %d, want 1", tc.status, stockMarks(t, d.DB))
		}
	}
}

// The acceptance test: a sale completes with the cloud unreachable, and the
// upload follows once it is back.
func TestASaleCompletesWithTheCloudUnreachableAndTheUploadFollows(t *testing.T) {
	d := openMigratedDB(t, "stocksale.db")
	seedStock(d.DB)
	// The check-in has already run once and the first upload was accepted.
	stock := &stockFeed{}
	url := stockServer(t, "cloud", stock)
	cfg := testCfg(url)
	if err := Tick(context.Background(), cfg, d.DB, Hooks{}); err != nil {
		t.Fatal(err)
	}
	// The stock endpoint goes down (the check-in itself still answers: this
	// is the harshest case for the upload; a fully dead cloud never reaches
	// the upload at all).
	stock.set(http.StatusServiceUnavailable, "stock_unavailable")
	saleID, err := pos.CompleteSale(context.Background(), d.DB, pos.SaleInput{
		SaleType: "sale", RegisterID: "reg-1", CashierID: "cashier-1", Currency: "GBP",
		Lines:    []pos.SaleLineInput{{ItemID: stockLocalItem, SKU: "S", Name: "Tea", Qty: 3, UnitPrice: 250, LocationID: "loc_main"}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: 750, Currency: "GBP"}},
	})
	if err != nil {
		t.Fatalf("the sale did not complete with the cloud's stock endpoint down: %v", err)
	}
	if saleID == "" {
		t.Fatal("no sale id")
	}
	if err := Tick(context.Background(), cfg, d.DB, Hooks{}); err != nil {
		t.Fatalf("Tick with the stock endpoint down: %v", err)
	}
	if stockMarks(t, d.DB) != 1 {
		t.Fatalf("the sale's stock change was not kept for upload: %d marks", stockMarks(t, d.DB))
	}
	// The cloud is back: the upload follows with the post-sale quantity.
	stock.set(0, "")
	if err := Tick(context.Background(), cfg, d.DB, Hooks{}); err != nil {
		t.Fatal(err)
	}
	reqs := stock.requests()
	if len(reqs) != 2 {
		t.Fatalf("stock requests = %d, want 2 (first sync + the follow-up)", len(reqs))
	}
	row := reqs[1]["rows"].([]any)[0].(map[string]any)
	if row["on_hand_milli"] != float64(5500) { // 8.5 - 3
		t.Fatalf("follow-up row = %v, want 5500", row)
	}
	if stockMarks(t, d.DB) != 0 {
		t.Fatal("marks left after the follow-up")
	}
}

func setRealtime(t *testing.T, db *sql.DB, tier string) {
	t.Helper()
	s := data.NewSettingsRepo(db)
	for k, v := range map[string]string{
		entitlement.KeyLastConfirmedAt: time.Now().UTC().Format(time.RFC3339),
		entitlement.KeyCloudLinkTier:   tier,
		entitlement.KeyCloudLinkMode:   "always",
	} {
		if err := s.Set(context.Background(), k, v); err != nil {
			t.Fatal(err)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStockUploadLoopDebouncesOnTheRealtimeTier(t *testing.T) {
	if stockUploadInterval > 5*time.Second {
		t.Fatalf("debounce interval %v exceeds the 5 s contract", stockUploadInterval)
	}
	d := openMigratedDB(t, "stockloop.db")
	seedStock(d.DB)
	setRealtime(t, d.DB, "realtime")
	// Make the cached catalogue mode cloud, as a check-in would.
	if err := data.NewSettingsRepo(d.DB).Set(context.Background(), entitlement.KeyCatalogMaster, "cloud"); err != nil {
		t.Fatal(err)
	}
	stock := &stockFeed{}
	url := stockServer(t, "cloud", stock)
	old := stockUploadInterval
	stockUploadInterval = 40 * time.Millisecond
	t.Cleanup(func() { stockUploadInterval = old })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); StockUploadLoop(ctx, testCfg(url), d.DB) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, "the first (backfill) upload", func() bool { return len(stock.requests()) == 1 })
	// A burst of sales inside one interval: coalesced into the latest value.
	for _, q := range []float64{8, 7, 6, 5} {
		if _, err := d.Exec(`UPDATE inventory SET quantity = ? WHERE item_id = ?`, q, stockLocalItem); err != nil {
			t.Fatal(err)
		}
	}
	// A tick can land between two of the burst's commits and upload a
	// mid-burst quantity, so wait for the latest value, not for a second
	// request (#4995).
	lastRow := func() map[string]any {
		reqs := stock.requests()
		if len(reqs) < 2 {
			return nil
		}
		return reqs[len(reqs)-1]["rows"].([]any)[0].(map[string]any)
	}
	waitFor(t, "an upload carrying the latest quantity", func() bool {
		r := lastRow()
		return r != nil && r["on_hand_milli"] == float64(5000)
	})
	reqs := stock.requests()
	for _, r := range reqs[1:] {
		if n := len(r["rows"].([]any)); n != 1 {
			t.Fatalf("a request carried %d rows for one inventory row", n)
		}
	}
}

func TestStockUploadLoopStaysQuietOnThePeriodicTierAndOnAReplica(t *testing.T) {
	for name, tc := range map[string]struct{ tier, primary string }{
		"periodic": {"periodic", ""},
		"replica":  {"realtime", "https://main-till.local:8443"},
	} {
		t.Run(name, func(t *testing.T) {
			d := openMigratedDB(t, "stockquiet.db")
			seedStock(d.DB)
			setRealtime(t, d.DB, tc.tier)
			s := data.NewSettingsRepo(d.DB)
			_ = s.Set(context.Background(), entitlement.KeyCatalogMaster, "cloud")
			if tc.primary != "" {
				_ = s.Set(context.Background(), "sync.primary_url", tc.primary)
			}
			stock := &stockFeed{}
			url := stockServer(t, "cloud", stock)
			old := stockUploadInterval
			stockUploadInterval = 10 * time.Millisecond
			t.Cleanup(func() { stockUploadInterval = old })
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			StockUploadLoop(ctx, testCfg(url), d.DB)
			if n := len(stock.requests()); n != 0 {
				t.Fatalf("%s: the loop uploaded (%d requests)", name, n)
			}
		})
	}
}

// Review fix (ut-docs#4763): marks that can never be sent (an item the cloud
// has no link for) must not keep the loop dialling the cloud every 5 s.
func TestStockUploadLoopMakesNoRequestForMarksThatCannotBeSent(t *testing.T) {
	d := openMigratedDB(t, "stockunlinked.db")
	for _, q := range []string{
		`INSERT INTO items (id, name, base_price) VALUES ('nolink', 'Tea', 250)`,
		`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-nl', 'nolink', 'loc_main', 3)`,
		// An orphan mark (its inventory row is gone): a Run would purge it, so
		// it is still here only if the loop never started a Run.
		`INSERT INTO items (id, name, base_price) VALUES ('orphan', 'Gone', 250)`,
		`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-or', 'orphan', 'loc_main', 3)`,
		`DELETE FROM inventory WHERE id = 'inv-or'`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if stockMarks(t, d.DB) == 0 {
		t.Fatal("setup: the unlinked row must carry a mark")
	}
	setRealtime(t, d.DB, "realtime")
	_ = data.NewSettingsRepo(d.DB).Set(context.Background(), entitlement.KeyCatalogMaster, "cloud")
	var hits int
	var mu sync.Mutex
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/stores/stock/levels" {
			mu.Lock()
			hits++
			mu.Unlock()
		}
		http.Error(w, "x", http.StatusInternalServerError)
	}))
	t.Cleanup(counting.Close)
	old := stockUploadInterval
	stockUploadInterval = 10 * time.Millisecond
	t.Cleanup(func() { stockUploadInterval = old })
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	StockUploadLoop(ctx, testCfg(counting.URL), d.DB)
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("the loop made %d stock requests for marks that cannot be sent", hits)
	}
	if stockMarks(t, d.DB) != 2 {
		t.Fatalf("%d marks left; the loop started upload runs although nothing was sendable", stockMarks(t, d.DB))
	}
}

// Review fix: one upload run is bounded, so a slow cloud cannot add minutes
// to a check-in (it was 20 requests x 30 s).
func TestUploadCloudStockIsBoundedByTheRunTimeout(t *testing.T) {
	d := openMigratedDB(t, "stockslow.db")
	seedStock(d.DB)
	_ = data.NewSettingsRepo(d.DB).Set(context.Background(), entitlement.KeyCatalogMaster, "cloud")
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); slow.Close() })
	old := stockUploadRunTimeout
	stockUploadRunTimeout = 200 * time.Millisecond
	t.Cleanup(func() { stockUploadRunTimeout = old })
	start := time.Now()
	failed := uploadCloudStock(context.Background(), testCfg(slow.URL), d.DB)
	if !failed || time.Since(start) > 3*time.Second {
		t.Fatalf("failed=%v after %v: the run was not bounded", failed, time.Since(start))
	}
	if stockMarks(t, d.DB) != 1 {
		t.Fatal("the mark must stay owed")
	}
}

func (f *stockFeed) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits
}

// Review round 2 (ut-docs#4763): a cloud that keeps answering older_stream is
// a failure the realtime loop backs off from, with every mark kept; it must
// not be dialled every interval, and must not rotate again and again.
func TestStockUploadLoopBacksOffWhenTheCloudKeepsAnsweringOlderStream(t *testing.T) {
	d := openMigratedDB(t, "stockolder.db")
	seedStock(d.DB)
	setRealtime(t, d.DB, "realtime")
	if err := data.NewSettingsRepo(d.DB).Set(context.Background(), entitlement.KeyCatalogMaster, "cloud"); err != nil {
		t.Fatal(err)
	}
	stock := &stockFeed{older: true}
	url := stockServer(t, "cloud", stock)
	old := stockUploadInterval
	stockUploadInterval = 20 * time.Millisecond
	t.Cleanup(func() { stockUploadInterval = old })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); StockUploadLoop(ctx, testCfg(url), d.DB) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, "the first run (a request, a rotation, a second request)", func() bool { return stock.requestCount() >= 2 })
	time.Sleep(400 * time.Millisecond) // twenty intervals
	if n := stock.requestCount(); n != 2 {
		t.Fatalf("%d requests: the loop did not back off after the stream conflict", n)
	}
	if stockMarks(t, d.DB) != 1 {
		t.Fatalf("marks = %d, want the one owed row kept", stockMarks(t, d.DB))
	}
	if !uploadCloudStock(context.Background(), testCfg(url), d.DB) {
		t.Fatal("a stream conflict must be reported as a failure (the caller backs off)")
	}
	if n := stock.requestCount(); n != 3 {
		t.Fatalf("a rate-limited run made %d requests in total, want 3 (one, no new rotation)", n)
	}
}
