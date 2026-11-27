package enroll

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

func fleetTestCfg(url string) *config.Config {
	return &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: url, StoreID: "store-1", MerchantToken: "tok-main"}}
}

// ut-docs#7098: an older cloud sends no removed_tills; the list still decodes.
func TestFleetWithRemovedOldCloud(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"devices":[{"device_id":"d1","till_id":"t1","name":"mac-till"}]},"error":null}`))
	}))
	defer srv.Close()
	devs, removed, err := FleetWithRemoved(context.Background(), fleetTestCfg(srv.URL))
	if err != nil || len(devs) != 1 || removed != nil {
		t.Fatalf("devs=%v removed=%v err=%v, want 1 device and nil removed", devs, removed, err)
	}
}

// A new cloud lists removed tills beside the live ones.
func TestFleetWithRemovedNewCloud(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"devices":[{"device_id":"d1","till_id":"t1","name":"mac-till"}],"removed_tills":[{"device_id":"d9","till_id":"t9","removed_at":"2026-11-25T19:48:00Z"}]},"error":null}`))
	}))
	defer srv.Close()
	_, removed, err := FleetWithRemoved(context.Background(), fleetTestCfg(srv.URL))
	if err != nil || len(removed) != 1 || removed[0].TillID != "t9" || removed[0].DeviceID != "d9" {
		t.Fatalf("removed=%+v err=%v, want t9/d9", removed, err)
	}
}

// RemoveFromAccount posts the store id and device id with the till's bearer;
// 2xx and 404 (already gone) are success, 409/500/offline are errors.
func TestRemoveFromAccount(t *testing.T) {
	var calls int32
	status := http.StatusOK
	var got struct {
		StoreID  string `json:"store_id"`
		DeviceID string `json:"device_id"`
	}
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/stores/devices/remove" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	cfg := fleetTestCfg(srv.URL)
	if err := RemoveFromAccount(context.Background(), cfg, "d9"); err != nil {
		t.Fatalf("200: %v", err)
	}
	if got.StoreID != "store-1" || got.DeviceID != "d9" || auth != "Bearer tok-main" {
		t.Fatalf("sent %+v auth %q", got, auth)
	}
	status = http.StatusNotFound
	if err := RemoveFromAccount(context.Background(), cfg, "d9"); err != nil {
		t.Fatalf("404 must count as already gone: %v", err)
	}
	for _, st := range []int{http.StatusInternalServerError, http.StatusConflict, http.StatusForbidden} {
		status = st
		if err := RemoveFromAccount(context.Background(), cfg, "d9"); err == nil {
			t.Fatalf("status %d must be an error", st)
		}
	}
	if err := RemoveFromAccount(context.Background(), cfg, ""); err == nil {
		t.Fatal("an empty device id must be refused before any call")
	}
	if n := atomic.LoadInt32(&calls); n != 5 {
		t.Fatalf("calls = %d, want 5 (the empty id makes none)", n)
	}
}

// The cloud answering 404 because the route is off is told apart from "gone"
// only by the till's flag, so a route that is not enabled never counts as done:
// ErrRemoveNotEnabled is returned when the body says so.
func TestRemoveFromAccountNotEnabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"data":null,"error":{"code":"not_enabled"}}`))
	}))
	defer srv.Close()
	if err := RemoveFromAccount(context.Background(), fleetTestCfg(srv.URL), "d9"); err != ErrRemoveNotEnabled {
		t.Fatalf("err = %v, want ErrRemoveNotEnabled", err)
	}
}

// New cloud, OLD till build: Fleet (the pre-#7098 reader, which knows nothing of
// removed_tills) gets a devices list without the removed rows and the extra
// field is ignored: a missing row is simply not present, never an error.
func TestFleetOldReaderAgainstNewCloudPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"devices":[{"device_id":"d1","till_id":"t1","name":"mac-till"}],"removed_tills":[{"device_id":"d9","till_id":"t9","removed_at":"2026-11-25T19:48:00Z"}]},"error":null}`))
	}))
	defer srv.Close()
	devs, err := Fleet(context.Background(), fleetTestCfg(srv.URL))
	if err != nil {
		t.Fatalf("old reader must not fail on the new payload: %v", err)
	}
	if len(devs) != 1 || devs[0].DeviceID != "d1" {
		t.Fatalf("devices = %+v, want only d1 (removed d9 absent)", devs)
	}
}
