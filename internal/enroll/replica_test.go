package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#2730: before the fix the admin sync copied the main till's
// marketplace identity onto every replica, so every till heartbeat as the
// main till's device with the main till's token. These tests pin the
// replica's own-identity lifecycle.

const (
	mainDeviceID = "till-7547e9b4-main"
	pinnedKey    = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
)

// seedCopiedReplica seeds kv the way a pre-fix replica looks after admin
// sync: the MAIN till's device id + registration marker + store token, and
// no own-identity marker.
func seedCopiedReplica(kv *fakeKV, primaryURL string) {
	for k, v := range map[string]string{
		"sync.primary_url":  primaryURL,
		"sync.bearer":       "replica-bearer",
		"sync.till_id":      "till-row-2",
		"sync.till_name":    "Back office",
		keyDeviceID:         mainDeviceID,
		keyDeviceRegistered: mainDeviceID,
		keyStoreID:          "store-abc",
		keyMerchantID:       "store-abc",
		keyToken:            "store-token",
		keyEnrolledAt:       "2026-09-03T17:16:22Z",
		keyPublicKey:        pinnedKey,
	} {
		_ = kv.Set(context.Background(), k, v)
	}
}

func fastRetries(t *testing.T) {
	t.Helper()
	oldDelays, oldInterval := replicaRetryDelays, vouchInterval
	replicaRetryDelays = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}
	vouchInterval = time.Hour
	t.Cleanup(func() { replicaRetryDelays, vouchInterval = oldDelays, oldInterval })
}

// fakePrimary serves POST /api/sync/cloud-device like a fixed main till.
// The first `fail` answers are failCode. leakToken makes it misbehave by
// adding a token to its answer (a buggy or hostile main till).
type fakePrimary struct {
	srv       *httptest.Server
	calls     atomic.Int32
	fail      int32
	failCode  int
	leakToken bool
	lastReqMu sync.Mutex
	lastReq   map[string]string
}

func newFakePrimary(t *testing.T, fail int32, failCode int) *fakePrimary {
	t.Helper()
	p := &fakePrimary{fail: fail, failCode: failCode}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sync/cloud-device", func(w http.ResponseWriter, r *http.Request) {
		n := p.calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer replica-bearer" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if n <= p.fail {
			w.WriteHeader(p.failCode)
			return
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		p.lastReqMu.Lock()
		p.lastReq = req
		p.lastReqMu.Unlock()
		data := map[string]string{"store_id": "store-abc", "device_id": req["device_id"]}
		if p.leakToken {
			data["token"] = "leaked-store-token"
			data["merchant_token"] = "leaked-store-token"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

// A replica carrying the main till's device id mints its own at boot,
// drops the copied registration markers, and records whose identity it is.
func TestInitReplicaWithCopiedIdentityMintsOwnDevice(t *testing.T) {
	resetState()
	kv := newFakeKV()
	seedCopiedReplica(kv, "http://127.0.0.1:1") // nothing listens: offline main till
	cfg := &config.Config{}                     // no marketplace endpoint: no network at all
	logging.ResetRecent()

	Init(context.Background(), cfg, kv, &sync.WaitGroup{})

	// ut-docs#2798: a successful self-repair is not a problem — it must not
	// reach the heartbeat's Problems ring (my.'s "Attention needed").
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "carried another till's cloud device identity") {
			t.Fatalf("self-repair logged as a problem: %+v", p)
		}
	}

	got := kv.get(keyDeviceID)
	if got == mainDeviceID || !strings.HasPrefix(got, "till-") {
		t.Fatalf("device_id = %q, want a freshly minted till-* distinct from the main till's", got)
	}
	if CurrentStatus().DeviceID != got || cfg.Marketplace.DeviceID != got {
		t.Fatalf("live device id not the repaired one: status=%q cfg=%q want %q", CurrentStatus().DeviceID, cfg.Marketplace.DeviceID, got)
	}
	if kv.get(keyDeviceTillID) != "till-row-2" {
		t.Fatalf("device_till_id = %q, want the replica's own sync.till_id", kv.get(keyDeviceTillID))
	}
	if kv.get(keyDeviceRegistered) != "" || kv.get(keyEnrolledAt) != "" {
		t.Fatalf("copied registration markers survived: registered=%q enrolled_at=%q", kv.get(keyDeviceRegistered), kv.get(keyEnrolledAt))
	}
	// A copy of the store token that already reached this replica stays
	// until the cloud can issue per-device tokens (see replica.go's file
	// comment): wiping it revokes nothing — the main till's copy is the same
	// credential — and would only cut this till off the cloud.
	if kv.get(keyToken) != "store-token" {
		t.Fatalf("token=%q, want the legacy copy kept", kv.get(keyToken))
	}

	// Stable across restarts: the repair runs once.
	resetState()
	Init(context.Background(), &config.Config{}, kv, &sync.WaitGroup{})
	if again := kv.get(keyDeviceID); again != got {
		t.Fatalf("second boot re-minted the device id: %q -> %q", got, again)
	}
}

// The main till (no sync.primary_url) is never touched.
func TestInitMainTillIdentityUntouched(t *testing.T) {
	resetState()
	kv := newFakeKV()
	seedCopiedReplica(kv, "")
	Init(context.Background(), &config.Config{}, kv, &sync.WaitGroup{})
	if kv.get(keyDeviceID) != mainDeviceID || kv.get(keyDeviceRegistered) != mainDeviceID || kv.get(keyDeviceTillID) != "" {
		t.Fatalf("main till identity changed: id=%q registered=%q till=%q", kv.get(keyDeviceID), kv.get(keyDeviceRegistered), kv.get(keyDeviceTillID))
	}
}

// An operator-pinned device id (UT_MARKETPLACE_DEVICE_ID) is never replaced.
func TestInitReplicaExplicitDeviceIDNotRepaired(t *testing.T) {
	resetState()
	kv := newFakeKV()
	seedCopiedReplica(kv, "http://127.0.0.1:1")
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{DeviceID: mainDeviceID}}
	Init(context.Background(), cfg, kv, &sync.WaitGroup{})
	if kv.get(keyDeviceID) != mainDeviceID {
		t.Fatalf("explicit device id replaced: %q", kv.get(keyDeviceID))
	}
}

// cloudRegisterCapture is a fake cloud serving /v1/stores/devices/register
// with the store token "store-token".
type cloudRegisterCapture struct {
	srv   *httptest.Server
	mu    sync.Mutex
	last  map[string]string
	calls atomic.Int32
}

func newFakeCloud(t *testing.T) *cloudRegisterCapture {
	t.Helper()
	c := &cloudRegisterCapture{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/stores/devices/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer store-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		c.mu.Lock()
		c.last = req
		c.mu.Unlock()
		c.calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"device_count": 2}})
	})
	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)
	return c
}

func (c *cloudRegisterCapture) lastReq() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// The repaired replica asks the main till to vouch for its own device id;
// the main till's answer carries no credential and the replica's token
// state is untouched.
func TestReplicaIsVouchedForByMainTill(t *testing.T) {
	resetState()
	fastRetries(t)
	primary := newFakePrimary(t, 0, 0)
	kv := newFakeKV()
	seedCopiedReplica(kv, primary.srv.URL)
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:1/api"}}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })

	Init(ctx, cfg, kv, &wg)

	own := kv.get(keyDeviceID)
	waitFor(t, "vouched registration", func() bool { return kv.get(keyDeviceRegistered) == own })
	primary.lastReqMu.Lock()
	sent := primary.lastReq
	primary.lastReqMu.Unlock()
	if sent["device_id"] != own || sent["version"] == "" {
		t.Fatalf("replica sent %v, want its own device id %q and its version", sent, own)
	}
	if _, has := sent["token"]; has {
		t.Fatal("replica sent a token to the main till")
	}
	if kv.get(keyToken) != "store-token" {
		t.Fatalf("token = %q, want the replica's token state untouched by a vouch", kv.get(keyToken))
	}
}

// Defence at the sink: even a main till that (buggy or hostile) puts a
// token in its answer never gets it persisted or made live on the replica.
func TestReplicaIgnoresTokenInVouchAnswer(t *testing.T) {
	resetState()
	fastRetries(t)
	primary := newFakePrimary(t, 0, 0)
	primary.leakToken = true
	kv := newFakeKV()
	for k, v := range map[string]string{
		"sync.primary_url": primary.srv.URL,
		"sync.bearer":      "replica-bearer",
		"sync.till_id":     "till-row-2",
		keyPublicKey:       pinnedKey,
	} {
		_ = kv.Set(context.Background(), k, v)
	}
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:1/api"}}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })

	Init(ctx, cfg, kv, &wg)
	own := kv.get(keyDeviceID)
	waitFor(t, "vouched registration", func() bool { return kv.get(keyDeviceRegistered) == own })
	if tok := kv.get(keyToken); tok != "" {
		t.Fatalf("replica persisted a token from the main till: %q", tok)
	}
	if eff := Effective(cfg); eff.Marketplace.MerchantToken != "" {
		t.Fatalf("replica made a token from the main till live: %q", eff.Marketplace.MerchantToken)
	}
}

// Main till unreachable at boot: nothing panics, the till keeps working,
// and the vouch lands on a later retry.
func TestReplicaVouchRetriesWhileMainTillOffline(t *testing.T) {
	resetState()
	fastRetries(t)
	primary := newFakePrimary(t, 3, http.StatusServiceUnavailable)
	kv := newFakeKV()
	seedCopiedReplica(kv, primary.srv.URL)
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:1/api"}}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })

	Init(ctx, cfg, kv, &wg)
	own := kv.get(keyDeviceID)
	waitFor(t, "vouch after retries", func() bool { return kv.get(keyDeviceRegistered) == own })
	if primary.calls.Load() < 4 {
		t.Fatalf("calls = %d, want ≥4 (3 failures then success)", primary.calls.Load())
	}
}

// Cancelling the context stops the loop even while the main till is down.
func TestReplicaVouchLoopStopsOnShutdown(t *testing.T) {
	resetState()
	fastRetries(t)
	kv := newFakeKV()
	seedCopiedReplica(kv, "http://127.0.0.1:1")
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:1/api"}}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	Init(ctx, cfg, kv, &wg)
	time.Sleep(30 * time.Millisecond)
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("vouch loop did not exit on shutdown")
	}
}

// A pre-fix main till has no vouch endpoint (404, or 401 from its session
// middleware): a replica that already holds a (legacy) token registers its
// own device id under the store with it, so the cloud still sees two
// distinct devices.
func TestReplicaFallsBackToSelfRegistrationWithOldMainTill(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusUnauthorized} {
		t.Run(http.StatusText(code), func(t *testing.T) { testReplicaFallsBackWithOldMainTill(t, code) })
	}
}

func testReplicaFallsBackWithOldMainTill(t *testing.T, code int) {
	resetState()
	fastRetries(t)
	primary := newFakePrimary(t, 1000, code)
	cloud := newFakeCloud(t)
	kv := newFakeKV()
	seedCopiedReplica(kv, primary.srv.URL)
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: cloud.srv.URL + "/api"}}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	Init(ctx, cfg, kv, &wg)

	own := kv.get(keyDeviceID)
	waitFor(t, "self registration", func() bool { return kv.get(keyDeviceRegistered) == own })
	if got := cloud.lastReq(); got["device_id"] != own || got["till_id"] != "till-row-2" {
		t.Fatalf("cloud saw %v, want device %q for till till-row-2", got, own)
	}
}

// A replica never creates its own anonymous store: RegisterNow asks the
// main till to vouch instead of calling /stores/register.
func TestRegisterNowOnReplicaUsesMainTillNotAnonymousStore(t *testing.T) {
	resetState()
	fastRetries(t)
	srv, registerCalls := testMarketplace(t, 0)
	primary := newFakePrimary(t, 0, 0)
	kv := newFakeKV()
	for k, v := range map[string]string{
		"sync.primary_url": primary.srv.URL,
		"sync.bearer":      "replica-bearer",
		"sync.till_id":     "till-row-2",
		keyPublicKey:       pinnedKey,
	} {
		_ = kv.Set(context.Background(), k, v)
	}
	cfg := freshConfig(srv.URL)
	cfg.Marketplace.PublicKey = pinnedKey
	Init(context.Background(), &config.Config{}, kv, &sync.WaitGroup{}) // identity only, no loop

	if _, err := RegisterNow(context.Background(), cfg, kv); err != nil {
		t.Fatalf("RegisterNow: %v", err)
	}
	if *registerCalls != 0 {
		t.Fatalf("replica created an anonymous store (%d /stores/register calls)", *registerCalls)
	}
	if own := kv.get(keyDeviceID); kv.get(keyDeviceRegistered) != own || primary.calls.Load() != 1 {
		t.Fatalf("device_registered=%q (own %q), main till asked %d times", kv.get(keyDeviceRegistered), own, primary.calls.Load())
	}
	if kv.get(keyToken) != "" {
		t.Fatalf("replica got a token: %q", kv.get(keyToken))
	}
}

// --- main-till side: vouching for a replica ------------------------------

func mainTillCfg(endpoint string) *config.Config {
	return &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: endpoint + "/api", StoreID: "store-abc", MerchantToken: "store-token", DeviceID: mainDeviceID,
		PublicKey: pinnedKey, // no background signing-key loop
	}}
}

// The main till registers the replica's own device id (with the replica's
// name from the main till's records, its version and its sync till id)
// under the store with the store token it alone holds. What it returns has
// no credential in it at all.
func TestVouchForReplicaRegistersDeviceWithoutHandingOverToken(t *testing.T) {
	resetState()
	cloud := newFakeCloud(t)
	cfg := mainTillCfg(cloud.srv.URL)
	kv := newFakeKV()
	_ = kv.Set(context.Background(), keyDeviceRegistered, mainDeviceID) // no own-device registration racing the capture
	Init(context.Background(), cfg, kv, &sync.WaitGroup{})

	v, err := VouchForReplica(context.Background(), cfg, ReplicaRequest{TillID: "till-row-2", DeviceID: "till-replica", DeviceName: "Back office", Version: "0.23.0"})
	if err != nil {
		t.Fatalf("vouch: %v", err)
	}
	if v.StoreID != "store-abc" || v.DeviceID != "till-replica" {
		t.Fatalf("vouch = %+v", v)
	}
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "store-token") {
		t.Fatalf("vouch answer carries the store token: %s", raw)
	}
	got := cloud.lastReq()
	if got["device_id"] != "till-replica" || got["device_name"] != "Back office" || got["version"] != "0.23.0" || got["till_id"] != "till-row-2" || got["store_id"] != "store-abc" {
		t.Fatalf("cloud register request = %v", got)
	}
}

func TestVouchForReplicaRefusals(t *testing.T) {
	resetState()
	cloud := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(cloud.Close)

	// Not registered yet: nothing to vouch with.
	unreg := &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: cloud.URL + "/api"}}
	Init(context.Background(), unreg, newFakeKV(), &sync.WaitGroup{})
	if _, err := VouchForReplica(context.Background(), unreg, ReplicaRequest{TillID: "t2", DeviceID: "till-x"}); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("unregistered main till: err = %v, want ErrNotRegistered", err)
	}

	resetState()
	cfg := mainTillCfg(cloud.URL)
	Init(context.Background(), cfg, newFakeKV(), &sync.WaitGroup{})
	for _, req := range []ReplicaRequest{
		{TillID: "t2", DeviceID: mainDeviceID}, // a replica may not claim the main till's device
		{TillID: "t2", DeviceID: ""},
		{TillID: "", DeviceID: "till-x"},
		{TillID: "t2", DeviceID: strings.Repeat("x", 200)},
	} {
		if _, err := VouchForReplica(context.Background(), cfg, req); !errors.Is(err, ErrBadDeviceRequest) {
			t.Fatalf("request %+v: err = %v, want ErrBadDeviceRequest", req, err)
		}
	}
}
