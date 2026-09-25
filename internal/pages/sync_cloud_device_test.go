package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
)

// ut-docs#2730: POST /api/sync/cloud-device — the main till registers a
// paired replica's OWN device id under the store with the store token only
// it holds. No credential ever crosses the LAN in the answer.

type cloudRegisterCapture struct {
	mu  sync.Mutex
	req map[string]string
}

// fakeCloud serves today's /v1/stores/devices/register, authenticated by the
// main till's store token.
func fakeCloud(t *testing.T) (*httptest.Server, *cloudRegisterCapture) {
	t.Helper()
	c := &cloudRegisterCapture{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/stores/devices/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer main-store-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["device_id"] == "till-main" {
			return // the main till's own background registration; not what these tests capture
		}
		c.mu.Lock()
		c.req = req
		c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"device_count": 2}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, c
}

// enrolMainTill makes the process-wide enrolment state that of a registered
// main till (restored to an unregistered blank on cleanup).
func enrolMainTill(t *testing.T, cloudURL string) *config.Config {
	t.Helper()
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{
		EndpointURL: cloudURL + "/api", StoreID: "store-abc", MerchantToken: "main-store-token",
		DeviceID: "till-main", PublicKey: strings.Repeat("ab", 32),
	}}
	enroll.Init(t.Context(), cfg, newMemKV(), &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	return cfg
}

// memKV is an in-memory enroll.Settings.
type memKV struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemKV() *memKV { return &memKV{m: map[string]string{}} }

func (k *memKV) Get(_ context.Context, key string) (string, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[key]
	return v, ok, nil
}

func (k *memKV) Set(_ context.Context, key, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = value
	return nil
}

func postCloudDevice(t *testing.T, mux *http.ServeMux, bearer, deviceID string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"device_id": deviceID, "version": "0.23.0"})
	req := httptest.NewRequest(http.MethodPost, "/api/sync/cloud-device", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSyncCloudDevice_RejectsUnauthorized(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	cloud, _ := fakeCloud(t)
	dp.Cfg = enrolMainTill(t, cloud.URL)
	mux := http.NewServeMux()
	registerSyncCloudDevice(mux, dp)
	for _, bearer := range []string{"", "token-nobody"} {
		rec := postCloudDevice(t, mux, bearer, "till-replica")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("bearer %q: status %d, want 401: %s", bearer, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "token") && strings.Contains(rec.Body.String(), "store-token") {
			t.Fatalf("unauthorized response leaked a token: %s", rec.Body.String())
		}
	}
}

func TestSyncCloudDevice_RegistersReplicaWithoutHandingOverToken(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	tillID, err := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Back office", hashBearer("token-abc"))
	if err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	cloud, capture := fakeCloud(t)
	dp.Cfg = enrolMainTill(t, cloud.URL)
	mux := http.NewServeMux()
	registerSyncCloudDevice(mux, dp)

	rec := postCloudDevice(t, mux, "token-abc", "till-replica")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data  map[string]any `json:"data"`
		Error any            `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Data["device_id"] != "till-replica" || out.Data["store_id"] != "store-abc" {
		t.Fatalf("answer = %v", out.Data)
	}
	// Security (ut-docs#2730): no token of any kind crosses the LAN.
	if strings.Contains(rec.Body.String(), "main-store-token") || strings.Contains(strings.ToLower(rec.Body.String()), "token") {
		t.Fatalf("answer carries a credential: %s", rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	// The name and till id come from the main till's own records, not the
	// replica's request body; the version is the replica's.
	if capture.req["till_id"] != tillID || capture.req["device_name"] != "Back office" || capture.req["device_id"] != "till-replica" || capture.req["version"] != "0.23.0" {
		t.Fatalf("cloud register request = %v, want till %s named Back office at 0.23.0", capture.req, tillID)
	}
}

func TestSyncCloudDevice_Refusals(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	if _, err := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Back office", hashBearer("token-abc")); err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	cloud, _ := fakeCloud(t)
	dp.Cfg = enrolMainTill(t, cloud.URL)
	mux := http.NewServeMux()
	registerSyncCloudDevice(mux, dp)

	// A replica may not claim the main till's own device id.
	if rec := postCloudDevice(t, mux, "token-abc", "till-main"); rec.Code != http.StatusBadRequest {
		t.Fatalf("main till's device id: status %d, want 400", rec.Code)
	}
	// A till that is itself a replica never vouches.
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://main:8080"); err != nil {
		t.Fatal(err)
	}
	if rec := postCloudDevice(t, mux, "token-abc", "till-replica"); rec.Code != http.StatusConflict {
		t.Fatalf("replica asked to vouch: status %d, want 409", rec.Code)
	}
}

// Reviewer finding (ut-docs#2730): every accepted vouch is one cloud call
// made by the MAIN till with its own token. Without a cap, a replica (or a
// compromised one) could make the main till hammer the cloud's
// devices/register — the cloud keeps no per-store device cap, and its
// per-IP limiter would then throttle the main till's OWN cloud traffic. The
// cap is per authenticated till (not per IP), so one noisy replica never
// blocks its siblings, and an unauthenticated caller never spends a till's
// budget.
func TestSyncCloudDevice_RateLimitedPerTill(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	repo := data.NewTillsRepo(dp.Db)
	for _, tl := range [][2]string{{"Back office", "token-abc"}, {"Bar", "token-bar"}} {
		if _, err := repo.InsertTill(t.Context(), tl[0], hashBearer(tl[1])); err != nil {
			t.Fatalf("enrol till: %v", err)
		}
	}
	cloud, _ := fakeCloud(t)
	dp.Cfg = enrolMainTill(t, cloud.URL)
	mux := http.NewServeMux()
	registerSyncCloudDevice(mux, dp)

	for i := 0; i < cloudDeviceVouchMax; i++ {
		if rec := postCloudDevice(t, mux, "token-abc", "till-replica"); rec.Code != http.StatusOK {
			t.Fatalf("call %d: status %d, want 200 within the cap: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := postCloudDevice(t, mux, "token-abc", "till-replica")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("call %d: status %d, want 429 past the cap: %s", cloudDeviceVouchMax+1, rec.Code, rec.Body.String())
	}
	// A sibling replica has its own budget.
	if rec := postCloudDevice(t, mux, "token-bar", "till-bar"); rec.Code != http.StatusOK {
		t.Fatalf("sibling till: status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Unauthenticated callers are refused before the cap and never spend
	// a till's budget: 401, not 429.
	if rec := postCloudDevice(t, mux, "token-nobody", "till-x"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown bearer: status %d, want 401", rec.Code)
	}
}

func TestSyncCloudDevice_MainTillNotRegistered(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	if _, err := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Back office", hashBearer("token-abc")); err != nil {
		t.Fatalf("enrol till: %v", err)
	}
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: "http://127.0.0.1:1/api", PublicKey: strings.Repeat("ab", 32)}}
	enroll.Init(t.Context(), cfg, newMemKV(), &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	dp.Cfg = cfg
	mux := http.NewServeMux()
	registerSyncCloudDevice(mux, dp)
	if rec := postCloudDevice(t, mux, "token-abc", "till-replica"); rec.Code != http.StatusConflict {
		t.Fatalf("unregistered main till: status %d, want 409: %s", rec.Code, rec.Body.String())
	}
}
