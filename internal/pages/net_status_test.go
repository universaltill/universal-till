package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/netreach"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3095: GET /ui/net-status feeds the status-bar light the cloud
// host's reachability — true, false, or null when unknown/disabled.

func getNetStatus(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/net-status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q, want no-store", cc)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if string(env["error"]) != "null" {
		t.Errorf("error = %s, want null", env["error"])
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(env["data"], &d); err != nil {
		t.Fatalf("data %s: %v", env["data"], err)
	}
	v, ok := d["cloud_reachable"]
	if !ok {
		t.Fatalf("data %s has no cloud_reachable", env["data"])
	}
	return string(v)
}

func TestNetStatusHandler(t *testing.T) {
	for _, c := range []struct {
		st   netreach.State
		want string
	}{
		{netreach.Unknown, "null"},
		{netreach.Reachable, "true"},
		{netreach.Unreachable, "false"},
	} {
		st := c.st
		if got := getNetStatus(t, netStatusHandler(func() netreach.State { return st })); got != c.want {
			t.Errorf("%v: cloud_reachable = %s, want %s", c.st, got, c.want)
		}
	}
}

func TestNetStatusRouteDisabledIsNull(t *testing.T) {
	for _, dp := range []*common.Deps{
		{}, // no monitor (bare-Deps tests)
		{NetReach: netreach.New(netreach.Options{Endpoint: "http://127.0.0.1:8081/api"})}, // dev/e2e default
	} {
		mux := http.NewServeMux()
		registerNetStatus(mux, dp)
		if got := getNetStatus(t, mux); got != "null" {
			t.Errorf("disabled monitor: cloud_reachable = %s, want null", got)
		}
	}
}
