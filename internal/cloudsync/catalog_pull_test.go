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

	"github.com/universaltill/universal-till/internal/catalogpull"
	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0162 §4 (ut-docs#4463): which ticks pull the cloud catalogue — only
// the main till, only when the cached catalog_master is "cloud", only after
// the check-in POST reached the cloud — and a pull failure never fails the
// check-in.

const pullItemID = "33333333-3333-4333-8333-000000000001"

// catalogFeed serves GET /v1/stores/catalog: one item at version 1, or the
// configured error status.
type catalogFeed struct {
	mu     sync.Mutex
	calls  int
	status int
	// price is the served item's price_minor (0 = 275); a negative one is
	// refused by the till and so recorded as an apply failure.
	price int64
	// reports are the bodies POSTed to …/catalog/apply-failures.
	reports [][]byte
	// reportStatus, when set, is the apply-failures endpoint's answer.
	reportStatus int
}

func (f *catalogFeed) report(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer tok-1" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reports = append(f.reports, b)
	status := f.reportStatus
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": map[string]any{"code": "apply_failures_unavailable", "message": "x"}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"stored": 1}, "error": nil})
}

func (f *catalogFeed) reportBodies() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.reports...)
}

func (f *catalogFeed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	status, price := f.status, f.price
	f.mu.Unlock()
	if price == 0 {
		price = 275
	}
	if r.Header.Get("Authorization") != "Bearer tok-1" || r.URL.Query().Get("store_id") != "store-1" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if status != 0 {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": map[string]string{"code": "catalog_unavailable"}})
		return
	}
	rows := []map[string]any{}
	if r.URL.Query().Get("since") == "0" {
		rows = append(rows, map[string]any{"kind": "item", "cloud_id": pullItemID, "version": 1, "deleted": false,
			"data": map[string]any{"name": "Cloud tea", "price_minor": price, "active": true}})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"rows": rows, "next": 1, "high_water": 1}, "error": nil})
}

func (f *catalogFeed) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// tickWithFeed runs one Tick against a fake cloud whose entitlement block
// carries the given catalog_master, plus the catalogue feed.
func tickWithFeed(t *testing.T, master string, primaryURL string, feedStatus int) (*catalogFeed, *fakeCloud, *data.SettingsRepo, func(string) bool, error) {
	t.Helper()
	return tickWithFeedOf(t, master, primaryURL, &catalogFeed{status: feedStatus})
}

func tickWithFeedOf(t *testing.T, master string, primaryURL string, feed *catalogFeed) (*catalogFeed, *fakeCloud, *data.SettingsRepo, func(string) bool, error) {
	t.Helper()
	return tickWithFeedSeeded(t, master, primaryURL, feed, nil)
}

// tickWithFeedSeeded is tickWithFeedOf with seed run on the migrated DB
// before the tick.
func tickWithFeedSeeded(t *testing.T, master string, primaryURL string, feed *catalogFeed, seed func(*sql.DB)) (*catalogFeed, *fakeCloud, *data.SettingsRepo, func(string) bool, error) {
	t.Helper()
	d := openMigratedDB(t, "pull.db")
	if seed != nil {
		seed(d.DB)
	}
	settings := data.NewSettingsRepo(d.DB)
	if primaryURL != "" {
		if err := settings.Set(context.Background(), "sync.primary_url", primaryURL); err != nil {
			t.Fatal(err)
		}
	}
	block := `{"plan":"shop","subscription_status":"active","catalog_master":"` + master + `"}`
	cloud := &fakeCloud{entitlement: json.RawMessage(block)}
	mux := http.NewServeMux()
	mux.Handle("/v1/stores/catalog", feed)
	mux.HandleFunc("/v1/stores/catalog/apply-failures", feed.report)
	mux.Handle("/", cloud.handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	err := Tick(context.Background(), testCfg(srv.URL), d.DB, Hooks{})
	has := func(id string) bool {
		var n int
		if qerr := d.DB.QueryRow(`SELECT COUNT(*) FROM items WHERE id = ?`, id).Scan(&n); qerr != nil {
			t.Fatal(qerr)
		}
		return n == 1
	}
	return feed, cloud, settings, has, err
}

func TestTickPullsTheCloudCatalogueOnACloudMasterMainTill(t *testing.T) {
	feed, cloud, settings, has, err := tickWithFeed(t, "cloud", "", 0)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if feed.count() == 0 {
		t.Fatal("a cloud-master main till did not pull")
	}
	if !has(pullItemID) {
		t.Fatal("pulled item not applied")
	}
	if v, _, _ := settings.Get(context.Background(), catalogpull.KeyCursor); v != "1" {
		t.Fatalf("cursor = %q, want 1", v)
	}
	// The changed catalogue is reported in the same tick (the snapshot
	// push's hash gate makes an unchanged one free).
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	found := false
	for _, snap := range cloud.snapshots {
		if items, ok := snap["items"].([]any); ok {
			for _, it := range items {
				if m, ok := it.(map[string]any); ok && m["id"] == pullItemID {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("no snapshot carried the pulled item (%d snapshots)", len(cloud.snapshots))
	}
}

func TestTickNeverPullsOnATillOrAdoptingStore(t *testing.T) {
	for _, master := range []string{"till", "adopting", "", "something-else"} {
		t.Run(master, func(t *testing.T) {
			feed, _, _, has, err := tickWithFeed(t, master, "", 0)
			if err != nil {
				t.Fatalf("Tick: %v", err)
			}
			if feed.count() != 0 || has(pullItemID) {
				t.Fatalf("catalog_master %q pulled (%d calls)", master, feed.count())
			}
		})
	}
}

func TestTickNeverPullsOnAReplica(t *testing.T) {
	feed, _, _, has, err := tickWithFeed(t, "cloud", "https://main-till.local:8443", 0)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if feed.count() != 0 || has(pullItemID) {
		t.Fatalf("a replica pulled the cloud catalogue (%d calls)", feed.count())
	}
}

func TestTickPullFailureNeverFailsTheCheckin(t *testing.T) {
	feed, _, settings, has, err := tickWithFeed(t, "cloud", "", http.StatusServiceUnavailable)
	if err != nil {
		t.Fatalf("a 503 from the feed failed the tick: %v", err)
	}
	if feed.count() == 0 || has(pullItemID) {
		t.Fatal("expected one refused pull and nothing applied")
	}
	if v, _, _ := settings.Get(context.Background(), catalogpull.KeyCursor); v != "0" && v != "" {
		t.Fatalf("cursor = %q after a 503, want unchanged (0)", v)
	}
}

// A 304 check-in (nothing changed: every catalogue write bumps
// link_version) skips the POST and so the pull — an idle shop adds no
// periodic feed request (ADR-0162 §4).
func TestTickDoesNotPullAfterA304Checkin(t *testing.T) {
	d := openMigratedDB(t, "pull304.db")
	cloud := &fakeCloud{entitlement: json.RawMessage(`{"plan":"shop","subscription_status":"active","catalog_master":"cloud"}`)}
	feed := &catalogFeed{}
	mux := http.NewServeMux()
	mux.Handle("/v1/stores/catalog", feed)
	mux.HandleFunc("/v1/stores/checkin", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"store-1:7"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"link_version": 7}})
	})
	mux.Handle("/", cloud.handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := testCfg(srv.URL)
	if err := Tick(context.Background(), cfg, d.DB, Hooks{}); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	first := feed.count()
	if first == 0 {
		t.Fatal("the first check-in (200 + POST) did not pull")
	}
	if err := Tick(context.Background(), cfg, d.DB, Hooks{}); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	cloud.mu.Lock()
	posts := len(cloud.syncBodies)
	cloud.mu.Unlock()
	if posts != 1 {
		t.Fatalf("sync POSTs = %d, want 1 (the second tick was a 304)", posts)
	}
	if feed.count() != first {
		t.Fatalf("a 304 tick pulled (%d → %d calls)", first, feed.count())
	}
}

// ut-docs#4493: after a served pull the main till reports the rows it
// refused; a pull the feed did not serve reports nothing, and a report
// failure never fails the check-in.
func TestTickReportsApplyFailuresAfterAServedPull(t *testing.T) {
	feed, _, settings, has, err := tickWithFeedOf(t, "cloud", "", &catalogFeed{price: -5})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if has(pullItemID) {
		t.Fatal("a negative price was applied")
	}
	bodies := feed.reportBodies()
	if len(bodies) != 1 {
		t.Fatalf("reports = %d, want 1", len(bodies))
	}
	var body struct {
		StoreID  string                `json:"store_id"`
		Failures []catalogpull.Failure `json:"failures"`
	}
	if err := json.Unmarshal(bodies[0], &body); err != nil {
		t.Fatalf("report body %s: %v", bodies[0], err)
	}
	if body.StoreID != "store-1" || len(body.Failures) != 1 || body.Failures[0].Kind != "item" ||
		body.Failures[0].CloudID != pullItemID || body.Failures[0].Version != 1 || body.Failures[0].Reason == "" {
		t.Fatalf("report = %s", bodies[0])
	}
	if v, _, _ := settings.Get(context.Background(), catalogpull.KeyFailuresReported); v == "" {
		t.Fatal("accepted report's fingerprint not stored")
	}
}

// seedApplyFailure records a refused cloud row, so a report has
// something to send.
func seedApplyFailure(t *testing.T) func(*sql.DB) {
	return func(db *sql.DB) {
		if err := data.NewCloudLinkRepo().RecordFailure(context.Background(), db, "item", pullItemID, 3, "price above the ceiling"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTickDoesNotReportWhenThePullWasNotServed(t *testing.T) {
	feed, _, _, _, err := tickWithFeedSeeded(t, "cloud", "", &catalogFeed{status: http.StatusServiceUnavailable}, seedApplyFailure(t))
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := len(feed.reportBodies()); n != 0 {
		t.Fatalf("reports = %d after an unserved pull with a recorded failure, want 0", n)
	}
}

// A 5xx from the report never fails the check-in and leaves the
// fingerprint unset, so the next served pull retries.
func TestTickReportServerErrorKeepsFingerprint(t *testing.T) {
	feed, _, settings, _, err := tickWithFeedSeeded(t, "cloud", "", &catalogFeed{price: -5, reportStatus: http.StatusServiceUnavailable}, nil)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := len(feed.reportBodies()); n != 1 {
		t.Fatalf("reports = %d, want 1 attempt", n)
	}
	if v, _, _ := settings.Get(context.Background(), catalogpull.KeyFailuresReported); v != "" {
		t.Fatalf("fingerprint %q stored after a 503", v)
	}
}
