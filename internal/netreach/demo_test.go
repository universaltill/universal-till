package netreach

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/netaccess"
)

// ADR-0113 §1.6 (ut-docs#2795 review finding F1, ut-docs#3588): the
// status-bar cloud-reachability probe never leaves the process on the
// public demo till.
func TestStatusMakesNoRequestInDemoMode(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	netaccess.SetDemo(true)
	t.Cleanup(func() { netaccess.SetDemo(false) })

	m := New(Options{Endpoint: srv.URL + "/api", Client: srv.Client(), allowLoopback: true})
	if m.Enabled() {
		t.Fatal("Monitor is enabled in demo mode, want disabled")
	}
	for i := 0; i < 3; i++ {
		if st := m.Status(); st != Unknown {
			t.Fatalf("Status = %v in demo mode, want Unknown", st)
		}
	}
	time.Sleep(20 * time.Millisecond) // let any stray probe goroutine land
	if n := hits.Load(); n != 0 {
		t.Fatalf("server saw %d request(s) in demo mode, want 0", n)
	}
}

// TestDefaultClientDeniedInDemoMode pins defence in depth: a Monitor built
// without an explicit Client — the production path, internal/pages/init.go
// — still refuses every request while demo mode is on, so a future
// regression in Enabled()'s demo check would still fail fast instead of
// silently leaking a request.
func TestDefaultClientDeniedInDemoMode(t *testing.T) {
	netaccess.SetDemo(true)
	t.Cleanup(func() { netaccess.SetDemo(false) })

	m := New(Options{Endpoint: "https://cloud.example.test/api"})
	_, err := m.client.Get("https://cloud.example.test/healthz")
	if !errors.Is(err, netaccess.ErrDemoDenied) {
		t.Fatalf("default client err = %v, want netaccess.ErrDemoDenied", err)
	}
}
