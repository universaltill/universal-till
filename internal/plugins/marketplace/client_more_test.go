package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

func testClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	cfg := &config.MarketplaceConfig{
		EndpointURL:       endpoint,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 5,
	}
	return NewClient(cfg, &mockTokenClient{token: "test-token"})
}

func TestResolveURL(t *testing.T) {
	c := testClient(t, "https://market.example.com")

	got, err := c.ResolveURL("/api/v1/downloads/artifact/abc")
	if err != nil {
		t.Fatalf("ResolveURL relative: %v", err)
	}
	if got != "https://market.example.com/api/v1/downloads/artifact/abc" {
		t.Fatalf("ResolveURL relative = %q", got)
	}

	abs := "https://cdn.example.com/bundle.tar.gz"
	got, err = c.ResolveURL(abs)
	if err != nil {
		t.Fatalf("ResolveURL absolute: %v", err)
	}
	if got != abs {
		t.Fatalf("ResolveURL absolute = %q; want unchanged", got)
	}

	bad := testClient(t, "://not-a-url")
	if _, err := bad.ResolveURL("/x"); err == nil {
		t.Fatal("ResolveURL with invalid endpoint: want error")
	}
}

func TestDeviceIDFromConfig(t *testing.T) {
	if got := DeviceIDFromConfig(nil); got != "pos-device-1" {
		t.Fatalf("DeviceIDFromConfig(nil) = %q", got)
	}
	if got := DeviceIDFromConfig(&config.MarketplaceConfig{DeviceID: "till-7"}); got != "till-7" {
		t.Fatalf("DeviceIDFromConfig(explicit) = %q", got)
	}
	// No explicit id: falls back to the hostname when one exists.
	hostname, _ := os.Hostname()
	if hostname == "" {
		t.Skip("no hostname on this machine")
	}
	if got := DeviceIDFromConfig(&config.MarketplaceConfig{}); got != hostname {
		t.Fatalf("DeviceIDFromConfig(fallback) = %q; want hostname %q", got, hostname)
	}
}

func TestAckDownload(t *testing.T) {
	var gotReq AckDownloadRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/download/ack" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode ack: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := testClient(t, server.URL)
	err := c.AckDownload(context.Background(), &AckDownloadRequest{
		PluginID: "p1", Version: "1.0.0", Token: "tok", Success: true,
	})
	if err != nil {
		t.Fatalf("AckDownload: %v", err)
	}
	if gotReq.PluginID != "p1" || !gotReq.Success {
		t.Fatalf("server saw %+v", gotReq)
	}
}

func TestAckDownloadServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	err := testClient(t, server.URL).AckDownload(context.Background(), &AckDownloadRequest{PluginID: "p1"})
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("AckDownload on 500 = %v; want status 500 error", err)
	}
}

func TestReportPluginStatusOptInPosts(t *testing.T) {
	var got ReportPluginStatusRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/telemetry/status" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode telemetry: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		APIVersion:        "1.0.0",
		RequestTimeoutSec: 5,
		TelemetryOptIn:    true,
	}
	c := NewClient(cfg, &mockTokenClient{token: "t"})
	err := c.ReportPluginStatus(context.Background(), &ReportPluginStatusRequest{
		Statuses: []PluginStatus{{PluginID: "p1", InstalledVersion: "1.0.0", Status: "enabled"}},
	})
	if err != nil {
		t.Fatalf("ReportPluginStatus: %v", err)
	}
	if len(got.Statuses) != 1 || got.Statuses[0].PluginID != "p1" {
		t.Fatalf("server saw %+v", got)
	}
}

func TestReportPluginStatusOptOutSendsNothing(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		RequestTimeoutSec: 5,
		TelemetryOptIn:    false,
	}
	c := NewClient(cfg, &mockTokenClient{token: "t"})
	if err := c.ReportPluginStatus(context.Background(), &ReportPluginStatusRequest{
		Statuses: []PluginStatus{{PluginID: "p1"}},
	}); err != nil {
		t.Fatalf("ReportPluginStatus opt-out: %v", err)
	}
	if requests != 0 {
		t.Fatalf("opted-out telemetry still sent %d request(s)", requests)
	}
}

func TestReportPluginStatusServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()

	cfg := &config.MarketplaceConfig{
		EndpointURL:       server.URL,
		RequestTimeoutSec: 5,
		TelemetryOptIn:    true,
	}
	c := NewClient(cfg, &mockTokenClient{token: "t"})
	err := c.ReportPluginStatus(context.Background(), &ReportPluginStatusRequest{})
	if err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("ReportPluginStatus on 502 = %v; want status 502 error", err)
	}
}

func TestGetRevocations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/revocations" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("since_version"); got != "42" {
			t.Errorf("since_version = %q; want 42", got)
		}
		json.NewEncoder(w).Encode(GetRevocationsResponse{
			Revocations: []Revocation{
				{PluginID: "bad-plugin", Version: "1.0.0", Action: "disable", Reason: "vuln"},
			},
			LatestVersion: 43,
		})
	}))
	defer server.Close()

	resp, err := testClient(t, server.URL).GetRevocations(context.Background(), &GetRevocationsRequest{SinceVersion: 42})
	if err != nil {
		t.Fatalf("GetRevocations: %v", err)
	}
	if resp.LatestVersion != 43 || len(resp.Revocations) != 1 || resp.Revocations[0].Action != "disable" {
		t.Fatalf("GetRevocations = %+v", resp)
	}
}

func TestGetRevocationsServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer server.Close()

	_, err := testClient(t, server.URL).GetRevocations(context.Background(), &GetRevocationsRequest{})
	if err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("GetRevocations on 404 = %v; want status 404 error", err)
	}
}

func TestIssueDownloadTokenErrorPaths(t *testing.T) {
	// Each subcase pins the distinct error message its branch produces, so a
	// regression that collapses one branch into another fails the test.
	cases := []struct {
		name    string
		wantErr string
		handler http.HandlerFunc
	}{
		{"non-200", "status 403", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "denied", http.StatusForbidden)
		}},
		{"error envelope", "listing revoked", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"error":{"message":"listing revoked"}}`))
		}},
		{"missing data", "missing response data", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}},
		{"bad json", "failed to decode", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`not json`))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			_, err := testClient(t, server.URL).IssueDownloadToken(context.Background(), &IssueDownloadTokenRequest{})
			if err == nil {
				t.Fatalf("%s: want error, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%s: error = %q; want it to contain %q", tc.name, err, tc.wantErr)
			}
		})
	}
}

// catalogServer's hit counter is an *int32 updated via sync/atomic, not a
// plain int — ut-docs#2143's background refresh means a stale-cache test
// can legitimately have the server handler goroutine still incrementing it
// while the test goroutine polls, and a plain int there is a real data race
// (caught by `go test -race`), not just a style nit.
func catalogServer(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"plugins": []map[string]any{
				{"listing_id": "p1", "name": "P1", "version": "1.0.0", "canonical_type": "payment"},
			},
		})
	}))
}

func TestGetOrFetchPrefersCache(t *testing.T) {
	var hits int32
	server := catalogServer(t, &hits)
	defer server.Close()

	repo, err := NewCatalogRepository(testClient(t, server.URL), t.TempDir())
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	ctx := context.Background()
	if _, err := repo.Fetch(ctx, "en", "linux/amd64"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	fetchesAfterWarm := atomic.LoadInt32(&hits)

	snap, stale, err := repo.GetOrFetch(ctx, "en", "linux/amd64")
	if err != nil {
		t.Fatalf("GetOrFetch: %v", err)
	}
	if stale {
		t.Fatal("fresh cache reported stale")
	}
	if len(snap.Plugins) != 1 {
		t.Fatalf("snapshot plugins = %d; want 1", len(snap.Plugins))
	}
	if got := atomic.LoadInt32(&hits); got != fetchesAfterWarm {
		t.Fatalf("GetOrFetch hit the network despite a warm cache (%d -> %d requests)", fetchesAfterWarm, got)
	}
}

func TestGetOrFetchFetchesWhenNoCache(t *testing.T) {
	var hits int32
	server := catalogServer(t, &hits)
	defer server.Close()

	repo, err := NewCatalogRepository(testClient(t, server.URL), t.TempDir())
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	snap, stale, err := repo.GetOrFetch(context.Background(), "en", "linux/amd64")
	if err != nil {
		t.Fatalf("GetOrFetch cold: %v", err)
	}
	if stale || len(snap.Plugins) != 1 || atomic.LoadInt32(&hits) == 0 {
		t.Fatalf("cold GetOrFetch: stale=%v plugins=%d hits=%d", stale, len(snap.Plugins), atomic.LoadInt32(&hits))
	}
}

func TestGetOrFetchErrorsWhenOfflineAndNoCache(t *testing.T) {
	// A closed server: connection refused, and nothing cached on disk.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	repo, err := NewCatalogRepository(testClient(t, url), t.TempDir())
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	if _, _, err := repo.GetOrFetch(context.Background(), "en", "linux/amd64"); err == nil {
		t.Fatal("GetOrFetch offline with no cache: want error")
	}
}

// ut-docs#2131: GetOrFetch used to compute isStale correctly (via Get()) and
// then ignore it — once a snapshot ever landed on disk, it was served
// forever no matter its age. These two tests cover the fix: a stale cache
// triggers a real refetch, and a refetch failure still falls back to the
// stale copy rather than erroring (offline-first is preserved).

// ut-docs#2143: GetOrFetch on a stale cache no longer refetches inline — it
// serves the stale snapshot immediately (so the caller never blocks on the
// network) and kicks the refetch off in the background. So "does staleness
// trigger a refetch" is now observed by polling for the background
// goroutine's effect, not by asserting on GetOrFetch's own return value.
func TestGetOrFetchRefetchesWhenStale(t *testing.T) {
	var hits int32
	server := catalogServer(t, &hits)
	defer server.Close()

	repo, err := NewCatalogRepository(testClient(t, server.URL), t.TempDir())
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	ctx := context.Background()
	if _, err := repo.Fetch(ctx, "en", "linux/amd64"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	fetchesAfterWarm := atomic.LoadInt32(&hits)

	// Force staleness without waiting out the real 15-minute window.
	repo.mu.Lock()
	repo.cached.FetchedAt = time.Now().Add(-2 * repo.staleAfter)
	repo.mu.Unlock()

	snap, stale, err := repo.GetOrFetch(ctx, "en", "linux/amd64")
	if err != nil {
		t.Fatalf("GetOrFetch: %v", err)
	}
	if !stale {
		t.Fatal("GetOrFetch should report the pre-refresh snapshot as stale — the refresh runs in the background, not inline")
	}
	if len(snap.Plugins) != 1 {
		t.Fatalf("snapshot plugins = %d; want 1", len(snap.Plugins))
	}

	// Poll on the cache's own staleness, not on the hit counter (review
	// Finding 1): `hits` is incremented at the START of the server
	// handler, before the background goroutine has updated cr.cached —
	// polling on `hits` reaching fetchesAfterWarm+1 could observe that
	// increment and proceed while the refresh was still in flight,
	// occasionally racing the `repo.Get()` call below (reproduced under
	// `-race`/high contention while building this fix). Waiting for the
	// cache to actually report fresh is what the test is really trying to
	// assert, and it can't fire early.
	var fresh *CatalogSnapshot
	var freshStale bool
	deadline := time.Now().Add(2 * time.Second)
	for {
		fresh, freshStale, err = repo.Get()
		if err != nil {
			t.Fatalf("Get after background refresh: %v", err)
		}
		if !freshStale {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("stale cache did not refresh in the background within the deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(fresh.Plugins) != 1 {
		t.Fatalf("refreshed snapshot plugins = %d; want 1", len(fresh.Plugins))
	}
	if got := atomic.LoadInt32(&hits); got != fetchesAfterWarm+1 {
		t.Fatalf("stale cache did not trigger exactly one background refetch (%d -> %d requests)", fetchesAfterWarm, got)
	}
}

// ut-docs#2131 review: the first cut of this test closed the server before
// forcing staleness, so a refetch attempt was indistinguishable from no
// refetch attempt at all -- `hits` stayed at 1 either way, and the test
// passed unchanged against the pre-fix GetOrFetch (which never attempted a
// refetch in the first place). Serving a real 500 on the second request
// instead means `hits` only reaches 2 if GetOrFetch genuinely tried and
// failed, which is what this test claims to verify.
//
// ut-docs#2143: the failed refetch now happens in the background (see
// TestGetOrFetchRefetchesWhenStale's doc comment above), so `hits` reaching
// 2 is polled for rather than observed synchronously.
func TestGetOrFetchFallsBackToStaleCacheWhenRefetchFails(t *testing.T) {
	var hits int32
	failAfterFirst := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if failAfterFirst {
			http.Error(w, "simulated marketplace outage", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"plugins": []map[string]any{
				{"listing_id": "p1", "name": "P1", "version": "1.0.0", "canonical_type": "payment"},
			},
		})
	}))
	defer server.Close()

	repo, err := NewCatalogRepository(testClient(t, server.URL), t.TempDir())
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	ctx := context.Background()
	if _, err := repo.Fetch(ctx, "en", "linux/amd64"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	failAfterFirst = true // any refetch attempt now fails with a real 500

	repo.mu.Lock()
	repo.cached.FetchedAt = time.Now().Add(-2 * repo.staleAfter)
	repo.mu.Unlock()

	snap, stale, err := repo.GetOrFetch(ctx, "en", "linux/amd64")
	if err != nil {
		t.Fatalf("GetOrFetch should fall back to the stale cache, not error: %v", err)
	}
	if !stale {
		t.Fatal("fallback snapshot should still be reported stale")
	}
	if len(snap.Plugins) != 1 {
		t.Fatalf("snapshot plugins = %d; want 1", len(snap.Plugins))
	}

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&hits) != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("expected the original fetch plus one failed background refetch attempt (2), got %d", got)
	}

	// The failed background refresh must not have clobbered the still-valid
	// stale cache.
	stillCached, stillStale, err := repo.Get()
	if err != nil {
		t.Fatalf("Get after failed background refresh: %v", err)
	}
	if !stillStale {
		t.Fatal("cache should still report stale after a failed background refresh")
	}
	if len(stillCached.Plugins) != 1 {
		t.Fatalf("snapshot plugins = %d; want 1", len(stillCached.Plugins))
	}
}

func TestGetReportsCorruptSnapshot(t *testing.T) {
	dir := t.TempDir()
	repo, err := NewCatalogRepository(testClient(t, "http://unused.example.com"), dir)
	if err != nil {
		t.Fatalf("NewCatalogRepository: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog-snapshot.json"), []byte("{corrupt"), 0o644); err != nil {
		t.Fatalf("write corrupt snapshot: %v", err)
	}
	if _, _, err := repo.Get(); err == nil {
		t.Fatal("Get with corrupt snapshot: want error")
	}
}

func TestNewCatalogRepositoryFailsWhenCacheDirIsAFile(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	if _, err := NewCatalogRepository(testClient(t, "http://unused.example.com"), blocked); err == nil {
		t.Fatal("NewCatalogRepository with a file as cache dir: want error")
	}
}
