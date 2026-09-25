package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#2673: the till's catalog request must use the gateway's real query
// keys (device_arch, host_version) — grpc-gateway silently ignores unknown
// ones — and must not show a listing that needs a newer till.

func compatTestClient(t *testing.T, plugins []PluginSummary, gotQuery *url.Values) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ListPluginsResponse{Plugins: plugins})
	}))
	t.Cleanup(server.Close)
	cfg := &config.MarketplaceConfig{EndpointURL: server.URL, APIVersion: "1.0.0", RequestTimeoutSec: 5}
	return NewClient(cfg, &mockTokenClient{token: "t"})
}

func withHostVersion(t *testing.T, v string) {
	t.Helper()
	old := hostVersion
	hostVersion = func() string { return v }
	t.Cleanup(func() { hostVersion = old })
}

func TestListPlugins_SendsGatewayQueryKeys(t *testing.T) {
	withHostVersion(t, "0.9.4")
	var q url.Values
	c := compatTestClient(t, nil, &q)

	if _, err := c.ListPlugins(context.Background(), &ListPluginsRequest{DeviceArch: "linux/amd64"}); err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if got := q.Get("device_arch"); got != "linux/amd64" {
		t.Errorf("device_arch = %q, want linux/amd64 (query: %v)", got, q)
	}
	if q.Has("arch") {
		t.Errorf("legacy arch key still sent — the gateway ignores it (query: %v)", q)
	}
	if got := q.Get("host_version"); got != "0.9.4" {
		t.Errorf("host_version = %q, want 0.9.4 (query: %v)", got, q)
	}
}

func TestListPlugins_ExplicitHostVersionWins(t *testing.T) {
	withHostVersion(t, "0.9.4")
	var q url.Values
	c := compatTestClient(t, nil, &q)

	if _, err := c.ListPlugins(context.Background(), &ListPluginsRequest{HostVersion: "1.2.3"}); err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if got := q.Get("host_version"); got != "1.2.3" {
		t.Errorf("host_version = %q, want 1.2.3", got)
	}
}

func TestListPlugins_DevBuildSendsNoHostVersion(t *testing.T) {
	// A dev/pre-release build has no comparable version; sending "dev"
	// would make the cloud fall back to a string compare.
	for _, v := range []string{"dev", "", "0.9.4-rc1", "abc"} {
		withHostVersion(t, v)
		var q url.Values
		c := compatTestClient(t, nil, &q)
		if _, err := c.ListPlugins(context.Background(), &ListPluginsRequest{}); err != nil {
			t.Fatalf("ListPlugins: %v", err)
		}
		if q.Has("host_version") {
			t.Errorf("version %q: host_version sent (%q), want omitted", v, q.Get("host_version"))
		}
	}
}

func TestListPlugins_DropsListingNeedingNewerHost(t *testing.T) {
	withHostVersion(t, "0.9.4")
	var q url.Values
	c := compatTestClient(t, []PluginSummary{
		{ID: "ok-older", MinHostVersion: "0.9.3"},
		{ID: "ok-equal", MinHostVersion: "0.9.4"},
		{ID: "ok-none"},
		{ID: "too-new-patch", MinHostVersion: "0.9.5"},
		{ID: "too-new-major", MinHostVersion: "1.0"},
		{ID: "too-new-v", MinHostVersion: "v0.10.0"},
		{ID: "ok-garbage", MinHostVersion: "latest"},
	}, &q)

	resp, err := c.ListPlugins(context.Background(), &ListPluginsRequest{})
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	var got []string
	for _, p := range resp.Plugins {
		got = append(got, p.ID)
	}
	want := []string{"ok-older", "ok-equal", "ok-none", "ok-garbage"}
	if len(got) != len(want) {
		t.Fatalf("plugins = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("plugins = %v, want %v", got, want)
		}
	}
}

func TestListPlugins_DevBuildKeepsEveryListing(t *testing.T) {
	withHostVersion(t, "dev")
	var q url.Values
	c := compatTestClient(t, []PluginSummary{{ID: "a", MinHostVersion: "99.0.0"}}, &q)

	resp, err := c.ListPlugins(context.Background(), &ListPluginsRequest{})
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(resp.Plugins) != 1 {
		t.Fatalf("dev build filtered the catalog: %d plugins, want 1", len(resp.Plugins))
	}
}

func TestPluginSummary_DecodesMinHostVersion(t *testing.T) {
	for _, raw := range []string{`{"id":"x","minHostVersion":"1.2.0"}`, `{"id":"x","min_host_version":"1.2.0"}`} {
		var p PluginSummary
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if p.MinHostVersion != "1.2.0" {
			t.Errorf("decode %s: MinHostVersion = %q, want 1.2.0", raw, p.MinHostVersion)
		}
	}
}
