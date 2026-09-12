package marketplace

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

func TestCatalogRepository_FetchAndGet(t *testing.T) {
	// Setup mock client
	mockToken := &mockTokenClient{token: "test-token"}
	// Start mock marketplace server
	mockPlugins := []map[string]interface{}{
		{
			"listing_id":     "test-plugin",
			"name":           "Test Plugin",
			"version":        "1.0.0",
			"artifact_url":   "http://example.com/plugin.tar.gz",
			"sha256":         "deadbeef",
			"canonical_type": "payment",
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"plugins": mockPlugins}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	tmpDir := t.TempDir()
	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	ctx := context.Background()
	snapshot, err := repo.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	if len(snapshot.Plugins) == 0 {
		t.Error("expected plugins in snapshot")
	}

	cached, isStale, err := repo.Get()
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if isStale {
		t.Error("fresh snapshot marked as stale")
	}

	if len(cached.Plugins) != len(snapshot.Plugins) {
		t.Errorf("cached plugin count mismatch: %d != %d", len(cached.Plugins), len(snapshot.Plugins))
	}
}

func TestCatalogRepository_StaleDetection(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"plugins": []map[string]interface{}{
			{
				"listing_id":     "test-plugin",
				"name":           "Test Plugin",
				"version":        "1.0.0",
				"artifact_url":   "http://example.com/plugin.tar.gz",
				"sha256":         "deadbeef",
				"canonical_type": "payment",
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	repo.staleAfter = 100 * time.Millisecond

	ctx := context.Background()
	_, err = repo.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	_, isStale, err := repo.Get()
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if !isStale {
		t.Error("expected catalog to be marked as stale")
	}
}

func TestCatalogRepository_DiskPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"plugins": []map[string]interface{}{
			{
				"listing_id":     "test-plugin",
				"name":           "Test Plugin",
				"version":        "1.0.0",
				"artifact_url":   "http://example.com/plugin.tar.gz",
				"sha256":         "deadbeef",
				"canonical_type": "payment",
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo1, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	ctx := context.Background()
	snapshot1, err := repo1.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	repo2, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	snapshot2, _, err := repo2.Get()
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if snapshot2.SnapshotVersion != snapshot1.SnapshotVersion {
		t.Error("snapshot version mismatch after disk reload")
	}
}

func TestCatalogRepository_Filter(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"plugins": []map[string]interface{}{
			{
				"listing_id":     "test-plugin",
				"name":           "Test Plugin",
				"version":        "1.0.0",
				"artifact_url":   "http://example.com/plugin.tar.gz",
				"sha256":         "deadbeef",
				"canonical_type": "payment",
			},
			{
				"listing_id":     "other-plugin",
				"name":           "Other Plugin",
				"version":        "1.0.0",
				"artifact_url":   "http://example.com/other.tar.gz",
				"sha256":         "cafebabe",
				"canonical_type": "report",
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	ctx := context.Background()
	_, err = repo.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	filtered, err := repo.Filter("payment", "", "")
	if err != nil {
		t.Fatalf("Filter failed: %v", err)
	}

	for _, p := range filtered {
		if p.CanonicalType != "payment" {
			t.Errorf("unexpected plugin type in filtered results: %s", p.CanonicalType)
		}
	}
}

func TestCatalogRepository_OfflineReplay(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"plugins": []map[string]interface{}{
			{
				"listing_id":     "test-plugin",
				"name":           "Test Plugin",
				"version":        "1.0.0",
				"artifact_url":   "http://example.com/plugin.tar.gz",
				"sha256":         "deadbeef",
				"canonical_type": "payment",
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}

	ctx := context.Background()
	_, err = repo.Fetch(ctx, "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	// Simulate offline by using invalid endpoint
	cfg.EndpointURL = "http://invalid-endpoint:9999"

	snapshot, isStale, err := repo.Get()
	if err != nil {
		t.Fatalf("Get failed when offline: %v", err)
	}

	if len(snapshot.Plugins) == 0 {
		t.Error("expected cached plugins when offline")
	}

	t.Logf("Offline snapshot available, stale=%v", isStale)
}

// TestCatalogRepository_GetOrFetch_StaleCacheReturnsImmediatelyOnUnreachableMarketplace
// is ut-docs#2143's regression test: a stale cache must be served promptly,
// with the refresh happening in the background, rather than GetOrFetch
// blocking the caller for the marketplace client's own request timeout.
func TestCatalogRepository_GetOrFetch_StaleCacheReturnsImmediatelyOnUnreachableMarketplace(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}

	// A dead/blackholed network route: the listener accepts the TCP
	// connection but never responds, so a request against it hangs until
	// the client's own RequestTimeoutSec elapses — ut-docs#2143's exact
	// failure scenario (a stale cache with no clean "connection refused").
	// Every accepted connection is tracked and closed on test cleanup
	// (review Finding 9) — otherwise the background refresh this test
	// triggers keeps a live connection open past the test's own return,
	// and its eventual timeout-driven log line bleeds into whichever test
	// happens to run next.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	var acceptedMu sync.Mutex
	var accepted []net.Conn
	t.Cleanup(func() {
		ln.Close()
		acceptedMu.Lock()
		defer acceptedMu.Unlock()
		for _, c := range accepted {
			c.Close()
		}
	})
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			// Accept and never write/close — client blocks until its own
			// timeout, never a fast connection-refused error.
			acceptedMu.Lock()
			accepted = append(accepted, conn)
			acceptedMu.Unlock()
		}
	}()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       "http://" + ln.Addr().String(),
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 1, // long enough that a synchronous block would be obvious, short enough to keep the background attempt's own log line from outliving the test by much
	}
	client := NewClient(cfg, mockToken)

	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}
	repo.staleAfter = time.Millisecond

	seeded := &CatalogSnapshot{
		Plugins:   []PluginSummary{{ID: "seed-plugin", Name: "Seed Plugin", Version: "1.0.0"}},
		FetchedAt: time.Now(),
	}
	if err := repo.saveSnapshot(seeded); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	repo.cached = seeded
	time.Sleep(2 * time.Millisecond) // let it go stale

	start := time.Now()
	snapshot, isStale, err := repo.GetOrFetch(context.Background(), "en-US", "linux/amd64")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("GetOrFetch returned error: %v", err)
	}
	if !isStale {
		t.Error("expected the stale cache to be reported as stale")
	}
	if snapshot == nil || len(snapshot.Plugins) != 1 || snapshot.Plugins[0].ID != "seed-plugin" {
		t.Errorf("expected the stale cached snapshot to be served immediately, got %+v", snapshot)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("GetOrFetch blocked for %v on an unreachable marketplace (RequestTimeoutSec=5s) — "+
			"should serve the stale cache immediately and refresh in the background", elapsed)
	}
}

// TestCatalogRepository_GetOrFetch_NoDuplicateConcurrentBackgroundRefresh is
// ut-docs#2143's second acceptance criterion: several requests arriving
// while the cache is stale must not each start their own network refresh.
//
// Review Finding 2: the first version of this test asserted only
// maxConcurrent <= 1, which a background refresh that never ran at all
// (maxConcurrent == 0, e.g. refreshInBackground silently disabled) also
// satisfies — confirmed by deliberately sabotaging refreshInBackground to
// a no-op, which the original assertion did not catch. It now requires
// totalRequests >= 1 (proving a refresh genuinely happened) and
// maxConcurrent == 1 exactly (proving it happened at most once), and waits
// for the first request to actually arrive before releasing it so the
// five concurrent callers have a real chance to race.
func TestCatalogRepository_GetOrFetch_NoDuplicateConcurrentBackgroundRefresh(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}

	var inFlight int32
	var maxConcurrent int32
	var totalRequests int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&totalRequests, 1)
		n := atomic.AddInt32(&inFlight, 1)
		for {
			cur := atomic.LoadInt32(&maxConcurrent)
			if n <= cur {
				break
			}
			if atomic.CompareAndSwapInt32(&maxConcurrent, cur, n) {
				break
			}
		}
		<-release
		atomic.AddInt32(&inFlight, -1)
		resp := map[string]interface{}{"plugins": []map[string]interface{}{
			{
				"listing_id":     "test-plugin",
				"name":           "Test Plugin",
				"version":        "1.0.0",
				"artifact_url":   "http://example.com/plugin.tar.gz",
				"sha256":         "deadbeef",
				"canonical_type": "payment",
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}
	repo.staleAfter = time.Millisecond

	seeded := &CatalogSnapshot{
		Plugins:   []PluginSummary{{ID: "seed-plugin", Name: "Seed Plugin", Version: "1.0.0"}},
		FetchedAt: time.Now(),
	}
	if err := repo.saveSnapshot(seeded); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	repo.cached = seeded
	time.Sleep(2 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := repo.GetOrFetch(context.Background(), "en-US", "linux/amd64"); err != nil {
				t.Errorf("GetOrFetch returned error: %v", err)
			}
		}()
	}
	wg.Wait()

	// Wait for the background refresh to actually reach the marketplace
	// before releasing it. Without this, close(release) below can run
	// before any of the 5 GetOrFetch calls' background goroutines have
	// even started their request, and the test would pass vacuously even
	// if the background refresh mechanism were fully disabled (Finding 2).
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&totalRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&totalRequests) == 0 {
		t.Fatal("expected the stale cache to trigger at least one background refresh request, got none")
	}

	close(release)

	// Let any in-flight background refresh actually finish before checking.
	deadline = time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&inFlight) > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&maxConcurrent); got != 1 {
		t.Errorf("expected exactly 1 concurrent background refresh reaching the marketplace, got %d", got)
	}
	if got := atomic.LoadInt32(&totalRequests); got != 1 {
		t.Errorf("expected exactly 1 total background refresh request (no duplicates once the first completes), got %d", got)
	}
}

// TestCatalogRepository_GetOrFetch_NoCacheAtAllCoalescesConcurrentCallers is
// review Finding 3: several GetOrFetch callers arriving before ANY snapshot
// has ever been cached (a fresh till's first /plugins-family page loads)
// used to each fire their own synchronous network request, serialized only
// by accident (Fetch used to hold cr.mu across the whole call) — removing
// that accidental serialization to fix the stale-cache blocking bug turned
// this into a genuine thundering herd against a possibly-unreachable
// marketplace. Identical concurrent requests (same locale/deviceArch) must
// coalesce into exactly one network round-trip, with every caller getting
// that one result.
func TestCatalogRepository_GetOrFetch_NoCacheAtAllCoalescesConcurrentCallers(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}

	var totalRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&totalRequests, 1)
		resp := map[string]interface{}{"plugins": []map[string]interface{}{
			{
				"listing_id":     "test-plugin",
				"name":           "Test Plugin",
				"version":        "1.0.0",
				"artifact_url":   "http://example.com/plugin.tar.gz",
				"sha256":         "deadbeef",
				"canonical_type": "payment",
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 30,
	}
	client := NewClient(cfg, mockToken)

	repo, err := NewCatalogRepository(client, tmpDir)
	if err != nil {
		t.Fatalf("NewCatalogRepository failed: %v", err)
	}
	// No seeded cache at all — this is the cold-start path.

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, isStale, err := repo.GetOrFetch(context.Background(), "en-US", "linux/amd64")
			if err != nil {
				t.Errorf("GetOrFetch returned error: %v", err)
				return
			}
			if isStale {
				t.Error("a freshly (coalesced-)fetched cold-start snapshot should never report stale")
			}
			if snapshot == nil || len(snapshot.Plugins) != 1 {
				t.Errorf("expected the coalesced fetch's snapshot, got %+v", snapshot)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&totalRequests); got != 1 {
		t.Errorf("expected 5 concurrent cold-start callers to coalesce into 1 network request, got %d", got)
	}
}
