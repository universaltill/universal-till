package cloudsync

import (
	"context"
	"encoding/json"
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
}

func (f *catalogFeed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	status := f.status
	f.mu.Unlock()
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
			"data": map[string]any{"name": "Cloud tea", "price_minor": 275, "active": true}})
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
	d := openMigratedDB(t, "pull.db")
	settings := data.NewSettingsRepo(d.DB)
	if primaryURL != "" {
		if err := settings.Set(context.Background(), "sync.primary_url", primaryURL); err != nil {
			t.Fatal(err)
		}
	}
	block := `{"plan":"shop","subscription_status":"active","catalog_master":"` + master + `"}`
	cloud := &fakeCloud{entitlement: json.RawMessage(block)}
	feed := &catalogFeed{status: feedStatus}
	mux := http.NewServeMux()
	mux.Handle("/v1/stores/catalog", feed)
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
