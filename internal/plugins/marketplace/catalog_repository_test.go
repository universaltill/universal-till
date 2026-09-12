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

// ut-docs#2149: Fetch used to read only page 1 of ListPlugins, unlike this
// package's two setup-wizard callers (resolveAndInstallBasePlugin,
// languagePackLocalesForListing), which both already page through
// next_page_token (ut-docs#2133/#1108). This snapshot backs the general
// catalog browse UI plus category/tax-code listings (internal/ui/buttons.go,
// internal/pages/plugin_settings_page.go), so a listing sorting past page 1
// as the catalog grows would be invisible everywhere those read from the
// snapshot, not just one bounded lookup. Two single-plugin pages, forcing
// the fetch to follow next_page_token to find the second one at all.
func TestCatalogRepository_FetchPagesThroughFullCatalog(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page_token") == "" {
			// camelCase nextPageToken/snapshotVersion (snapshotVersion as a
			// quoted string) mirrors the real live wire format, not the
			// legacy snake_case fields — see ListPluginsResponse.UnmarshalJSON's
			// own doc comment. A test using only the legacy shape would still
			// pass even if the live-wire decode path (ut-docs#1108) broke,
			// since PluginSummary/ListPluginsResponse decode either shape
			// today; camelCase is what actually exercises that path.
			json.NewEncoder(w).Encode(map[string]interface{}{
				"plugins": []map[string]interface{}{
					{"listing_id": "page1-plugin", "name": "Page 1 Plugin", "version": "1.0.0", "canonical_type": "payment"},
				},
				"nextPageToken":   "page2",
				"snapshotVersion": "7",
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"plugins": []map[string]interface{}{
				{"listing_id": "page2-plugin", "name": "Page 2 Plugin", "version": "1.0.0", "canonical_type": "report"},
			},
		})
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

	snapshot, err := repo.Fetch(context.Background(), "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if hits != 2 {
		t.Fatalf("expected 2 catalog requests to page through 2 listings, got %d", hits)
	}
	if len(snapshot.Plugins) != 2 {
		t.Fatalf("expected 2 plugins across both pages in the snapshot, got %d", len(snapshot.Plugins))
	}
	if snapshot.SnapshotVersion != 7 {
		t.Errorf("SnapshotVersion = %d, want 7 (from the first page's response)", snapshot.SnapshotVersion)
	}

	filtered, err := repo.Filter("report", "", "")
	if err != nil {
		t.Fatalf("Filter failed: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ListingID != "page2-plugin" {
		t.Fatalf("expected page2-plugin (beyond page 1) to be findable via Filter — an unpaginated fetch would silently miss it, got %+v", filtered)
	}
}

// The pagination loop must still terminate against a server that keeps
// returning a non-empty next_page_token forever (malformed or hostile) —
// bounded the same way its two package siblings are, by catalogFetchMaxPages.
func TestCatalogRepository_FetchPaginationCapPreventsInfiniteLoop(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		// camelCase nextPageToken, same reasoning as the sibling test above.
		json.NewEncoder(w).Encode(map[string]interface{}{
			"plugins": []map[string]interface{}{
				{"listing_id": "listing", "name": "Listing", "version": "1.0.0", "canonical_type": "payment"},
			},
			"nextPageToken": "always-more",
		})
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

	done := make(chan error, 1)
	go func() {
		_, ferr := repo.Fetch(context.Background(), "en-US", "linux/amd64")
		done <- ferr
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Fetch failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Fetch did not return — pagination cap did not bound the loop")
	}
	if hits != catalogFetchMaxPages {
		t.Fatalf("expected exactly %d catalog requests (cap), got %d", catalogFetchMaxPages, hits)
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

// ut-docs#2155: two independent callers can both be mid-flight in Fetch at
// once (server.go's scheduler calls Fetch directly; a page handler's
// GetOrFetch spawns refreshInBackground/coldFetch). Each stamps its own
// FetchedAt with time.Now() BEFORE racing for cr.mu, so the fetch whose
// FetchedAt is numerically OLDER can acquire the lock SECOND and
// unconditionally overwrite a chronologically newer, already-cached
// snapshot with staler data. This test reproduces that race outcome
// deterministically by seeding cr.cached (white-box) with a snapshot whose
// FetchedAt is in the future, then running a real Fetch: the fetch's own
// return value must still be its own honest answer, but neither the memory
// cache nor the on-disk snapshot may be replaced by the older result.
func TestCatalogRepository_Fetch_NeverOverwritesCacheWithOlderResult(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"plugins": []map[string]interface{}{
			{
				"listing_id":     "older-fetch-plugin",
				"name":           "older-fetch-plugin",
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

	// Seed a cache entry that is chronologically NEWER than anything this
	// test's Fetch can produce (its FetchedAt is stamped from time.Now()
	// during the call, which is strictly before now+1h). Different
	// locale/arch than the fetch below, so a clobber is unambiguous.
	seeded := &CatalogSnapshot{
		Plugins: []PluginSummary{{
			ListingID: "already-cached-newer-plugin",
			Name:      "already-cached-newer-plugin",
			Version:   "9.9.9",
		}},
		SnapshotVersion: 42,
		FetchedAt:       time.Now().Add(1 * time.Hour),
		Locale:          "de-DE",
		DeviceArch:      "linux/arm64",
	}
	repo.mu.Lock()
	repo.cached = seeded
	repo.mu.Unlock()

	got, err := repo.Fetch(context.Background(), "en-US", "linux/amd64")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	// AC2: the caller always gets its OWN request's result, whether or not
	// the cache write was skipped — never another caller's snapshot.
	if got == nil || len(got.Plugins) != 1 || got.Plugins[0].Name != "older-fetch-plugin" {
		t.Fatalf("Fetch must return its own request's result; got %+v", got)
	}
	if got.Locale != "en-US" || got.DeviceArch != "linux/amd64" {
		t.Errorf("Fetch return value carries wrong params: locale=%q arch=%q", got.Locale, got.DeviceArch)
	}

	// The seeded newer entry must be completely untouched in memory.
	cached, _, err := repo.Get()
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if cached != seeded {
		t.Errorf("Fetch overwrote a chronologically newer cached snapshot with an older result: cached=%+v", cached)
	}
	if len(cached.Plugins) != 1 || cached.Plugins[0].Name != "already-cached-newer-plugin" {
		t.Errorf("cached plugins changed: %+v", cached.Plugins)
	}
	if cached.Locale != "de-DE" || cached.DeviceArch != "linux/arm64" || cached.SnapshotVersion != 42 {
		t.Errorf("cached snapshot fields changed: locale=%q arch=%q version=%d", cached.Locale, cached.DeviceArch, cached.SnapshotVersion)
	}
	if !cached.FetchedAt.Equal(seeded.FetchedAt) {
		t.Errorf("cached FetchedAt changed: %v != %v", cached.FetchedAt, seeded.FetchedAt)
	}

	// The disk write is guarded together with the memory write — nothing
	// was persisted, so the on-disk snapshot must still be absent.
	onDisk, err := repo.loadSnapshot()
	if err != nil {
		t.Fatalf("loadSnapshot failed: %v", err)
	}
	if onDisk != nil {
		t.Errorf("Fetch persisted an older result to disk despite a newer cached snapshot: %+v", onDisk)
	}
}

// ut-docs#2155, AC2: two concurrent Fetch calls with DIFFERENT
// (locale, deviceArch) parameters must each return the snapshot produced by
// their own request — never the other caller's — regardless of which one
// wins the race for cr.mu. The server staggers its responses (en-US/amd64
// is delayed 50ms, de-DE/arm64 answers immediately) to make the two
// requests genuinely overlap rather than serialize by accident, and returns
// a distinguishable plugin per branch so a swapped result is detectable.
// This does NOT reproduce the older-fetch-clobbers-newer race itself (that
// requires goroutine scheduling to reorder lock acquisition relative to
// FetchedAt, which this test doesn't force either way — it already passes
// pre-fix, since Fetch's return value was never the thing that raced) — it
// only guards the AC2 invariant that neither caller's direct answer is ever
// swapped for the other's. See
// TestCatalogRepository_Fetch_NeverOverwritesCacheWithOlderResult for the
// actual regression test that fails pre-fix.
func TestCatalogRepository_Fetch_ConcurrentDifferentParamsEachCallerGetsOwnResult(t *testing.T) {
	tmpDir := t.TempDir()
	mockToken := &mockTokenClient{token: "test-token"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		locale := r.URL.Query().Get("locale")
		arch := r.URL.Query().Get("arch")
		var name string
		switch {
		case locale == "en-US" && arch == "linux/amd64":
			name = "en-amd64-plugin"
			time.Sleep(50 * time.Millisecond)
		case locale == "de-DE" && arch == "linux/arm64":
			name = "de-arm64-plugin"
		default:
			t.Errorf("unexpected request params: locale=%q arch=%q", locale, arch)
			http.Error(w, "unexpected params", http.StatusBadRequest)
			return
		}
		resp := map[string]interface{}{"plugins": []map[string]interface{}{
			{
				"listing_id":     name,
				"name":           name,
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

	type call struct {
		locale, arch, wantPlugin string
	}
	calls := []call{
		{locale: "en-US", arch: "linux/amd64", wantPlugin: "en-amd64-plugin"},
		{locale: "de-DE", arch: "linux/arm64", wantPlugin: "de-arm64-plugin"},
	}
	results := make([]*CatalogSnapshot, len(calls))
	errs := make([]error, len(calls))

	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Add(1)
		go func(i int, c call) {
			defer wg.Done()
			results[i], errs[i] = repo.Fetch(context.Background(), c.locale, c.arch)
		}(i, c)
	}
	wg.Wait()

	for i, c := range calls {
		if errs[i] != nil {
			t.Errorf("Fetch(%s, %s) returned error: %v", c.locale, c.arch, errs[i])
			continue
		}
		got := results[i]
		if got == nil {
			t.Errorf("Fetch(%s, %s) returned nil snapshot", c.locale, c.arch)
			continue
		}
		if got.Locale != c.locale || got.DeviceArch != c.arch {
			t.Errorf("Fetch(%s, %s) returned another caller's params: locale=%q arch=%q", c.locale, c.arch, got.Locale, got.DeviceArch)
		}
		if len(got.Plugins) != 1 || got.Plugins[0].Name != c.wantPlugin {
			t.Errorf("Fetch(%s, %s) returned another caller's plugins: want %q, got %+v", c.locale, c.arch, c.wantPlugin, got.Plugins)
		}
	}
}
