package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

func TestTokenClient_GetToken_Success(t *testing.T) {
	// Setup mock OAuth server

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/merchant-token" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var reqBody map[string]string
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if reqBody["merchant_id"] != "test-client" {
			t.Errorf("expected merchant_id 'test-client', got %s", reqBody["merchant_id"])
		}
		if reqBody["device_id"] != "test-device-1" {
			t.Errorf("expected device_id 'test-device-1', got %s", reqBody["device_id"])
		}
		resp := TokenResponse{
			Token:     "test-token-12345",
			ExpiresAt: time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			Scope:     "marketplace:read",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Setup client with temp cache
	tmpDir := t.TempDir()
	cfg := &config.MarketplaceConfig{
		EndpointURL:  server.URL,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		DeviceID:     "test-device-1",
	}
	client := NewTokenClient(cfg)
	client.cachePath = filepath.Join(tmpDir, "token.json")

	// Test getting token
	ctx := context.Background()
	token, err := client.GetToken(ctx)
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if token != "test-token-12345" {
		t.Errorf("expected token test-token-12345, got %s", token)
	}

	// Verify cached in memory
	if client.cached == nil {
		t.Error("token not cached in memory")
	}

	// Verify cached on disk
	data, err := os.ReadFile(client.cachePath)
	if err != nil {
		t.Fatalf("failed to read cache file: %v", err)
	}
	var cached CachedToken
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatalf("failed to unmarshal cache: %v", err)
	}
	if cached.Token != "test-token-12345" {
		t.Errorf("cached token mismatch: got %s", cached.Token)
	}
}

func TestTokenClient_GetToken_UsesCache(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := TokenResponse{
			AccessToken: "cached-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	cfg := &config.MarketplaceConfig{
		EndpointURL:  server.URL,
		ClientID:     "test",
		ClientSecret: "secret",
	}
	client := NewTokenClient(cfg)
	client.cachePath = filepath.Join(tmpDir, "token.json")

	ctx := context.Background()
	// First call - should hit server
	token1, err := client.GetToken(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Second call - should use cache
	token2, err := client.GetToken(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if token1 != token2 {
		t.Errorf("tokens don't match: %s != %s", token1, token2)
	}
	if callCount != 1 {
		t.Errorf("expected 1 server call, got %d", callCount)
	}
}

func TestTokenClient_GetToken_MissingCredentials(t *testing.T) {
	cfg := &config.MarketplaceConfig{
		EndpointURL: "http://example.com",
		// ClientID and ClientSecret intentionally empty
	}
	client := NewTokenClient(cfg)
	ctx := context.Background()

	_, err := client.GetToken(ctx)
	if err == nil {
		t.Error("expected error for missing credentials")
	}
	if err.Error() != "oauth2 token request failed: marketplace OAuth2 credentials not configured" {
		t.Errorf("unexpected error: %v", err)
	}
}

// GetToken must coalesce concurrent callers on a cold cache into exactly
// ONE network request, not one per goroutine (a thundering herd on the
// marketplace's token endpoint) — whichever of GetToken's three redundant
// layers (in-memory double-check, disk-cache fallback, or simply losing
// the race to acquire the lock first) ends up doing the work in any given
// run, the aggregate guarantee must hold. See
// TestTokenClient_GetToken_DoubleCheckAfterLock below for a deterministic
// drive of the in-memory double-check specifically.
func TestTokenClient_GetToken_ConcurrentSingleFlight(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(30 * time.Millisecond) // widen the race window
		resp := TokenResponse{
			Token:     "shared-token",
			ExpiresAt: time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	cfg := &config.MarketplaceConfig{EndpointURL: server.URL, ClientID: "c", ClientSecret: "s", DeviceID: "d"}
	client := NewTokenClient(cfg)
	client.cachePath = filepath.Join(tmpDir, "token.json")

	const n = 8
	var wg sync.WaitGroup
	tokens := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tokens[i], errs[i] = client.GetToken(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if tokens[i] != "shared-token" {
			t.Errorf("goroutine %d: token = %q", i, tokens[i])
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server called %d times, want exactly 1 (concurrent callers must coalesce)", got)
	}
}

// Deterministically drives GetToken's write-lock double-check (the branch
// right after acquiring tc.mu.Lock(), before falling to disk/network) —
// the concurrent single-flight test above proves the aggregate property but
// can't guarantee this exact branch runs, since whichever goroutine wins
// the race to acquire the lock first varies with scheduling. Here we hold
// tc.mu.Lock() ourselves before spawning any callers, so every goroutine's
// first RLock() call genuinely blocks and they're released to evaluate it
// together only once we Unlock — guaranteeing at least one late arrival
// finds tc.cached already populated by the time it gets the write lock.
func TestTokenClient_GetToken_DoubleCheckAfterLock(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(20 * time.Millisecond)
		resp := TokenResponse{Token: "barrier-token", ExpiresAt: time.Now().Add(1 * time.Hour).Format(time.RFC3339)}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	cfg := &config.MarketplaceConfig{EndpointURL: server.URL, ClientID: "c", ClientSecret: "s", DeviceID: "d"}
	client := NewTokenClient(cfg)
	client.cachePath = filepath.Join(tmpDir, "token.json")

	client.mu.Lock() // block every goroutine's initial RLock() below
	const n = 8
	var wg sync.WaitGroup
	tokens := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tokens[i], errs[i] = client.GetToken(context.Background())
		}(i)
	}
	time.Sleep(50 * time.Millisecond) // let all 8 genuinely queue on RLock()
	client.mu.Unlock()                // release them together: real contention on Lock()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if tokens[i] != "barrier-token" {
			t.Errorf("goroutine %d: token = %q", i, tokens[i])
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server called %d times, want exactly 1", got)
	}
}

// A valid, unexpired token already sitting on disk (e.g. from a previous
// process) must be picked up without hitting the network, even though the
// in-memory cache starts empty.
func TestTokenClient_GetToken_UsesDiskCacheWhenMemoryEmpty(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "token.json")
	onDisk := CachedToken{Token: "disk-token", ExpiresAt: time.Now().Add(1 * time.Hour)}
	data, _ := json.Marshal(onDisk)
	if err := os.WriteFile(cachePath, data, 0600); err != nil {
		t.Fatalf("seed disk cache: %v", err)
	}

	cfg := &config.MarketplaceConfig{EndpointURL: server.URL, ClientID: "c", ClientSecret: "s"}
	client := NewTokenClient(cfg)
	client.cachePath = cachePath

	token, err := client.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if token != "disk-token" {
		t.Errorf("token = %q, want disk-token", token)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Error("a valid on-disk token must not trigger a network request")
	}
}

// A disk cache file that fails to parse (corrupted, truncated, foreign
// format) must be treated as absent — fall through to a fresh network
// request rather than failing GetToken outright.
func TestTokenClient_GetToken_CorruptDiskCacheFallsThroughToNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := TokenResponse{Token: "fresh-token", ExpiresAt: time.Now().Add(1 * time.Hour).Format(time.RFC3339)}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "token.json")
	if err := os.WriteFile(cachePath, []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("seed corrupt disk cache: %v", err)
	}

	cfg := &config.MarketplaceConfig{EndpointURL: server.URL, ClientID: "c", ClientSecret: "s"}
	client := NewTokenClient(cfg)
	client.cachePath = cachePath

	token, err := client.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if token != "fresh-token" {
		t.Errorf("token = %q, want fresh-token (fallback to network)", token)
	}
}

// saveToDisk failing (e.g. the cache directory can't be created) must not
// fail GetToken — the token is already in hand, disk caching is best-effort.
func TestTokenClient_GetToken_SaveToDiskFailureIsNonFatal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := TokenResponse{Token: "tok", ExpiresAt: time.Now().Add(1 * time.Hour).Format(time.RFC3339)}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	blocker := filepath.Join(tmpDir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}

	cfg := &config.MarketplaceConfig{EndpointURL: server.URL, ClientID: "c", ClientSecret: "s"}
	client := NewTokenClient(cfg)
	client.cachePath = filepath.Join(blocker, "sub", "token.json") // "blocker" is a file, not a dir

	token, err := client.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken must still succeed despite an unwritable cache path: %v", err)
	}
	if token != "tok" {
		t.Errorf("token = %q", token)
	}
}

func newRawClient(cfg *config.MarketplaceConfig) *TokenClient {
	return &TokenClient{cfg: cfg, client: &http.Client{Timeout: 5 * time.Second}}
}

func TestRequestToken_MalformedEndpointURL(t *testing.T) {
	cfg := &config.MarketplaceConfig{EndpointURL: "http://example.com\n", ClientID: "c", ClientSecret: "s"}
	if _, err := newRawClient(cfg).requestToken(context.Background()); err == nil {
		t.Fatal("want a request-construction error for a control-character endpoint URL")
	}
}

func TestRequestToken_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // connection refused from here on
	cfg := &config.MarketplaceConfig{EndpointURL: srv.URL, ClientID: "c", ClientSecret: "s"}
	if _, err := newRawClient(cfg).requestToken(context.Background()); err == nil {
		t.Fatal("want a transport error when the marketplace is unreachable")
	}
}

func TestRequestToken_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := &config.MarketplaceConfig{EndpointURL: srv.URL, ClientID: "c", ClientSecret: "s"}
	if _, err := newRawClient(cfg).requestToken(context.Background()); err == nil {
		t.Fatal("want an error for a non-200 token response")
	}
}

func TestRequestToken_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	cfg := &config.MarketplaceConfig{EndpointURL: srv.URL, ClientID: "c", ClientSecret: "s"}
	if _, err := newRawClient(cfg).requestToken(context.Background()); err == nil {
		t.Fatal("want a decode error for an unparseable token response body")
	}
}

// A marketplace response with an expires_at that isn't valid RFC3339 must
// surface as an error rather than silently caching a zero-value expiry
// (which would make the token look permanently expired or permanently
// valid, depending on the zero value's relation to time.Now()).
func TestRequestToken_InvalidExpiresAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := TokenResponse{Token: "tok", ExpiresAt: "not-a-timestamp"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	cfg := &config.MarketplaceConfig{EndpointURL: srv.URL, ClientID: "c", ClientSecret: "s"}
	if _, err := newRawClient(cfg).requestToken(context.Background()); err == nil {
		t.Fatal("want an error for an unparseable expires_at")
	}
}

// A response with no expiry information at all (no expiresAt/expires_at,
// no expires_in) must default to a sane 1-hour-ish expiry rather than
// erroring or leaving the token expired-on-arrival.
func TestRequestToken_DefaultExpiryWhenNoExpiryFieldsGiven(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := TokenResponse{Token: "tok"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	cfg := &config.MarketplaceConfig{EndpointURL: srv.URL, ClientID: "c", ClientSecret: "s"}
	before := time.Now()
	cached, err := newRawClient(cfg).requestToken(context.Background())
	after := time.Now()
	if err != nil {
		t.Fatalf("requestToken: %v", err)
	}
	// The production default is exactly Now().Add(55*time.Minute); bracket
	// it tightly against the call's own before/after timestamps so a drift
	// to some other constant (e.g. 52min) would actually fail this test.
	wantMin := before.Add(55 * time.Minute)
	wantMax := after.Add(55 * time.Minute)
	if cached.ExpiresAt.Before(wantMin) || cached.ExpiresAt.After(wantMax) {
		t.Errorf("default ExpiresAt = %v, want between %v and %v (Now()+55min)", cached.ExpiresAt, wantMin, wantMax)
	}
}

// With no configured DeviceID, getDeviceID falls back to the machine's real
// hostname rather than the config value.
func TestGetDeviceID_FallsBackToHostname(t *testing.T) {
	hostname, _ := os.Hostname()
	if hostname == "" {
		t.Skip("os.Hostname() unavailable in this environment")
	}
	client := newRawClient(&config.MarketplaceConfig{})
	if got := client.getDeviceID(); got != hostname {
		t.Errorf("getDeviceID() = %q, want hostname %q", got, hostname)
	}
}

func TestSaveToDisk_MkdirAllError(t *testing.T) {
	tmpDir := t.TempDir()
	blocker := filepath.Join(tmpDir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	client := &TokenClient{cachePath: filepath.Join(blocker, "sub", "token.json")}
	err := client.saveToDisk(&CachedToken{Token: "t", ExpiresAt: time.Now()})
	if err == nil {
		t.Fatal("want an error when the cache directory can't be created")
	}
}

// ClearCache must propagate a genuine removal failure (not a bare "file
// doesn't exist") — e.g. the cache path unexpectedly points at a non-empty
// directory instead of a file.
func TestClearCache_PropagatesNonNotExistRemoveError(t *testing.T) {
	tmpDir := t.TempDir()
	asDir := filepath.Join(tmpDir, "token.json")
	if err := os.MkdirAll(asDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(asDir, "child"), []byte("x"), 0644); err != nil {
		t.Fatalf("seed child file: %v", err)
	}
	client := &TokenClient{cachePath: asDir}
	if err := client.ClearCache(); err == nil {
		t.Fatal("want ClearCache to propagate a non-empty-directory removal error")
	}
}

func TestTokenClient_ClearCache(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "token.json")
	client := &TokenClient{
		cachePath: cachePath,
		cached: &CachedToken{
			Token:     "test",
			ExpiresAt: time.Now().Add(1 * time.Hour),
		},
	}

	// Save to disk
	if err := client.saveToDisk(client.cached); err != nil {
		t.Fatal(err)
	}

	// Clear cache
	if err := client.ClearCache(); err != nil {
		t.Fatal(err)
	}

	// Verify memory cache cleared
	if client.cached != nil {
		t.Error("memory cache not cleared")
	}

	// Verify disk cache removed
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Error("disk cache file still exists")
	}
}
