package enroll

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

// fakeKV is an in-memory Settings implementation.
type fakeKV struct {
	mu sync.Mutex
	m  map[string]string
}

func newFakeKV() *fakeKV { return &fakeKV{m: map[string]string{}} }

func (f *fakeKV) Get(_ context.Context, key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[key]
	return v, ok, nil
}

func (f *fakeKV) Set(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[key] = value
	return nil
}

func (f *fakeKV) get(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[key]
}

// resetState clears the package-level identity between tests.
func resetState() {
	mu.Lock()
	cur = identity{}
	storeIDExplicit = false
	explicitConfigured = false
	displayStoreID = ""
	mu.Unlock()
}

// testMarketplace serves the register + signing-key endpoints the way the real
// marketplace mounts them: register under the /api base, signing-key on the
// sibling /ui path.
func testMarketplace(t *testing.T, failures int) (*httptest.Server, *int) {
	t.Helper()
	calls := new(int)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/stores/register", func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if *calls <= failures {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["device_id"] == "" {
			http.Error(w, "device_id required", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{
			"store_id":    "store-abc",
			"merchant_id": "store-abc",
			"device_id":   req["device_id"],
			"token":       "tok-123",
		}})
	})
	mux.HandleFunc("/ui/api/signing-key", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{
			"algorithm":      "ed25519",
			"public_key_hex": strings.Repeat("ab", 32),
		}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, calls
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func freshConfig(endpoint string) *config.Config {
	return &config.Config{
		StoreName: "Corner Shop",
		Marketplace: config.MarketplaceConfig{
			EndpointURL: endpoint + "/api",
			StoreID:     "Corner Shop", // config default: falls back to the store name
		},
	}
}

// Registration is LAZY (multi-store / paid cloud are the paid tier): a fresh
// till boot mints a device id and fetches the signing key but must NOT create
// a store; the first plugin download/install (EnsureRegistered) does.
func TestInitFreshTillDoesNotRegisterStore(t *testing.T) {
	resetState()
	srv, calls := testMarketplace(t, 0)
	kv := newFakeKV()
	cfg := freshConfig(srv.URL)

	Init(context.Background(), cfg, kv, &sync.WaitGroup{})

	deviceID := kv.get(keyDeviceID)
	if !strings.HasPrefix(deviceID, "till-") {
		t.Fatalf("device_id = %q, want generated till-*", deviceID)
	}
	if cfg.Marketplace.DeviceID != deviceID {
		t.Fatalf("cfg.DeviceID = %q, want %q", cfg.Marketplace.DeviceID, deviceID)
	}

	// The signing key still arrives (plugins must verify), the store doesn't.
	waitFor(t, "signing key", func() bool { return kv.get(keyPublicKey) != "" })
	time.Sleep(50 * time.Millisecond)
	if *calls != 0 {
		t.Fatalf("register called %d times at boot; registration must be lazy", *calls)
	}
	if kv.get(keyStoreID) != "" || kv.get(keyToken) != "" {
		t.Fatalf("store identity persisted at boot: %q/%q", kv.get(keyStoreID), kv.get(keyToken))
	}
	if CurrentStatus().Registered {
		t.Fatal("fresh till reports registered")
	}

	// First marketplace interaction: EnsureRegistered enrols and the live
	// identity reaches request handlers without a restart.
	eff := EnsureRegistered(context.Background(), cfg, kv)
	if *calls != 1 {
		t.Fatalf("register calls = %d, want 1", *calls)
	}
	if got := kv.get(keyStoreID); got != "store-abc" {
		t.Fatalf("persisted store_id = %q", got)
	}
	if eff.Marketplace.ClientID != "store-abc" || eff.Marketplace.StoreID != "store-abc" {
		t.Fatalf("effective identity = %q/%q, want store-abc", eff.Marketplace.ClientID, eff.Marketplace.StoreID)
	}
	if eff.Marketplace.MerchantToken != "tok-123" {
		t.Fatalf("effective merchant token = %q", eff.Marketplace.MerchantToken)
	}

	// Once registered, EnsureRegistered is a read-only pass-through.
	_ = EnsureRegistered(context.Background(), cfg, kv)
	if *calls != 1 {
		t.Fatalf("register calls after re-ensure = %d, want 1", *calls)
	}
	// The shared config was not mutated after startup (race-free handlers).
	if cfg.Marketplace.ClientID != "" {
		t.Fatalf("shared cfg mutated post-startup: ClientID = %q", cfg.Marketplace.ClientID)
	}
}

// Every marketplace mint REPLACES the store's claim code, so a second button
// click used to silently invalidate the code the operator was reading off the
// screen. Repeat calls inside the reuse window must return the SAME code
// without another mint; after the window a fresh code is minted.
func TestClaimCodeReusedWithinWindow(t *testing.T) {
	resetState()
	claimCodeCache.Lock()
	claimCodeCache.storeID, claimCodeCache.info, claimCodeCache.until = "", ClaimInfo{}, time.Time{}
	claimCodeCache.Unlock()

	mints := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/stores/claim-code", func(w http.ResponseWriter, r *http.Request) {
		mints++
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{
			"code":       map[int]string{1: "AAAA-1111", 2: "BBBB-2222"}[mints],
			"expires_at": time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339),
			"claim_path": "/ui/claim?store=store-abc",
		}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL:   srv.URL + "/api",
		StoreID:       "store-abc",
		MerchantToken: "tok-123",
	}}

	first, err := ClaimCode(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := ClaimCode(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if mints != 1 || first.Code != "AAAA-1111" || second.Code != "AAAA-1111" {
		t.Fatalf("repeat click should reuse the code: mints=%d first=%q second=%q", mints, first.Code, second.Code)
	}
	if !strings.Contains(first.ClaimURL, "/ui/claim?store=store-abc") {
		t.Fatalf("claim url = %q", first.ClaimURL)
	}

	// Window elapsed → a fresh code is minted.
	claimCodeCache.Lock()
	claimCodeCache.until = time.Now().Add(-time.Second)
	claimCodeCache.Unlock()
	third, err := ClaimCode(context.Background(), cfg)
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	if mints != 2 || third.Code != "BBBB-2222" {
		t.Fatalf("expired window should re-mint: mints=%d third=%q", mints, third.Code)
	}
}

func TestInitAlreadyEnrolledSkipsRegistration(t *testing.T) {
	resetState()
	srv, calls := testMarketplace(t, 0)
	kv := newFakeKV()
	seed := map[string]string{
		keyDeviceID:   "till-existing",
		keyStoreID:    "store-old",
		keyMerchantID: "store-old",
		keyToken:      "tok-old",
		keyPublicKey:  strings.Repeat("cd", 32),
	}
	for k, v := range seed {
		_ = kv.Set(context.Background(), k, v)
	}
	cfg := freshConfig(srv.URL)

	Init(context.Background(), cfg, kv, &sync.WaitGroup{})

	if cfg.Marketplace.ClientID != "store-old" || cfg.Marketplace.StoreID != "store-old" {
		t.Fatalf("cfg not filled from persisted enrolment: %q/%q", cfg.Marketplace.ClientID, cfg.Marketplace.StoreID)
	}
	if cfg.Marketplace.DeviceID != "till-existing" {
		t.Fatalf("cfg.DeviceID = %q", cfg.Marketplace.DeviceID)
	}
	time.Sleep(50 * time.Millisecond)
	if *calls != 0 {
		t.Fatalf("register called %d times for an already-enrolled till", *calls)
	}
}

func TestInitExplicitConfigWinsAndSkipsEnrolment(t *testing.T) {
	resetState()
	srv, calls := testMarketplace(t, 0)
	kv := newFakeKV()
	cfg := freshConfig(srv.URL)
	cfg.Marketplace.ClientID = "merchant-1"
	cfg.Marketplace.StoreID = "store-1"
	cfg.Marketplace.DeviceID = "pos-device-1"
	cfg.Marketplace.PublicKey = strings.Repeat("ef", 32)
	t.Setenv("UT_MARKETPLACE_STORE_ID", "store-1")

	Init(context.Background(), cfg, kv, &sync.WaitGroup{})

	time.Sleep(50 * time.Millisecond)
	if *calls != 0 {
		t.Fatalf("register called %d times despite explicit config", *calls)
	}
	eff := Effective(cfg)
	if eff.Marketplace.ClientID != "merchant-1" || eff.Marketplace.StoreID != "store-1" {
		t.Fatalf("explicit identity overridden: %q/%q", eff.Marketplace.ClientID, eff.Marketplace.StoreID)
	}
	// The explicit device_id is adopted as this till's persistent one.
	if got := kv.get(keyDeviceID); got != "pos-device-1" {
		t.Fatalf("persisted device_id = %q, want pos-device-1", got)
	}
}

// Lazy registration retries per interaction: a failed attempt leaves the till
// unregistered and the next EnsureRegistered tries again.
func TestEnsureRegisteredRetriesAcrossAttempts(t *testing.T) {
	resetState()
	srv, calls := testMarketplace(t, 2) // first two register calls fail
	kv := newFakeKV()
	cfg := freshConfig(srv.URL)
	Init(context.Background(), cfg, kv, &sync.WaitGroup{})

	for i := 1; i <= 3; i++ {
		eff := EnsureRegistered(context.Background(), cfg, kv)
		if i < 3 && eff.Marketplace.MerchantToken != "" {
			t.Fatalf("attempt %d: unexpectedly registered", i)
		}
		if i == 3 && eff.Marketplace.MerchantToken != "tok-123" {
			t.Fatalf("attempt 3: not registered, token = %q", eff.Marketplace.MerchantToken)
		}
	}
	if *calls != 3 {
		t.Fatalf("register calls = %d, want 3", *calls)
	}
}

func TestInitKeylessExplicitTillStillFetchesSigningKey(t *testing.T) {
	resetState()
	srv, calls := testMarketplace(t, 0)
	kv := newFakeKV()
	cfg := freshConfig(srv.URL)
	cfg.Marketplace.ClientID = "merchant-1" // explicit merchant, but no public key

	Init(context.Background(), cfg, kv, &sync.WaitGroup{})

	waitFor(t, "signing key", func() bool { return kv.get(keyPublicKey) != "" })
	time.Sleep(50 * time.Millisecond)
	if *calls != 0 {
		t.Fatalf("register called %d times despite explicit merchant", *calls)
	}
	if eff := Effective(cfg); eff.Marketplace.PublicKey != strings.Repeat("ab", 32) {
		t.Fatalf("effective public key = %q", eff.Marketplace.PublicKey)
	}
}

// Init's background registration loop must be genuinely joinable: wg.Wait()
// must NOT return while the loop is still in flight (a missing wg.Add would
// let Wait return immediately, vacuously "passing" without ever tracking the
// goroutine), and MUST return promptly once ctx is cancelled (a missing
// wg.Done, or a goroutine that ignores cancellation, would hang it forever).
// Both directions matter: this is what lets a caller (app.Run, at shutdown)
// safely assume nothing this goroutine touches is still live once Wait
// returns. Uses a handler that blocks until the request's own context is
// cancelled, so the fetch is provably still in flight until then, and a
// channel the handler closes on entry so the test knows the goroutine has
// genuinely reached the blocked call before asserting anything.
func TestInit_BackgroundLoopJoinsOnCancel(t *testing.T) {
	resetState()
	reqReceived := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/ui/api/signing-key", func(w http.ResponseWriter, r *http.Request) {
		close(reqReceived)
		<-r.Context().Done() // never respond on its own — only ctx cancel ends this
	})
	srv := httptest.NewServer(mux)
	// CloseClientConnections before Close: the handler goroutine above is
	// parked on <-r.Context().Done(), which only fires once the underlying
	// connection is force-closed — without this, a FAILING run of this test
	// (e.g. the join regressing) leaves that handler blocked until the
	// client's own timeout, and srv.Close alone waits it out (~15s stall
	// before reporting the actual failure).
	t.Cleanup(srv.Close)
	t.Cleanup(srv.CloseClientConnections)

	kv := newFakeKV()
	cfg := freshConfig(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	Init(ctx, cfg, kv, &wg)

	select {
	case <-reqReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("background loop never reached the signing-key request")
	}

	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	// The request is still blocked server-side — wg.Wait() must NOT be done yet.
	select {
	case <-waitDone:
		t.Fatal("wg.Wait() returned while the background loop was still in flight — goroutine not tracked")
	case <-time.After(150 * time.Millisecond):
	}

	cancel()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Init's background loop did not exit within 2s of ctx cancel")
	}
}

func TestFetchSigningKeyRejectsBadKey(t *testing.T) {
	resetState()
	mux := http.NewServeMux()
	mux.HandleFunc("/ui/api/signing-key", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{
			"algorithm":      "ed25519",
			"public_key_hex": "not-hex",
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	kv := newFakeKV()
	if err := fetchSigningKey(context.Background(), srv.URL+"/api", kv); err == nil {
		t.Fatal("fetchSigningKey accepted an invalid key")
	}
	if kv.get(keyPublicKey) != "" {
		t.Fatal("invalid key was persisted")
	}
}
