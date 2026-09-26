package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/universaltill/universal-till/internal/cloudlink"
	"github.com/universaltill/universal-till/internal/fleetlink"
)

// fakeCloudLink stands in for ut-cloud's GET /v1/tills/link (ADR-0117,
// ut-docs#2824) — just enough of the handshake to hand this package's
// refund/return/replica-ingest call sites a REAL *cloudlink.Client and
// observe the "sale" frames they actually put on the wire. cloudlink.Client
// is a concrete type with no seam to mock, and its own transport-level test
// harness (internal/cloudlink/helpers_test.go) lives in _test.go files —
// invisible to importers — so this is a small, purpose-built copy of just
// the handshake + frame-capture cloudlink's own tests already rely on
// (ut-docs#2894).
type fakeCloudLink struct {
	srv      *httptest.Server
	liveView bool

	connectOnce sync.Once
	connected   chan struct{}

	mu    sync.Mutex
	sales []map[string]any
}

func newFakeCloudLink(t *testing.T, liveView bool) *fakeCloudLink {
	t.Helper()
	f := &fakeCloudLink{liveView: liveView, connected: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/tills/link", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		hello, _ := json.Marshal(map[string]any{"link_version": 0, "live_view": f.liveView})
		env, _ := fleetlink.Encode(fleetlink.Envelope{ID: "h1", Type: "hello", Payload: hello})
		if err := ws.Write(r.Context(), websocket.MessageText, env); err != nil {
			return
		}
		f.connectOnce.Do(func() { close(f.connected) })
		for {
			_, b, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			e, err := fleetlink.Decode(b)
			if err != nil || e.Type != "sale" {
				continue
			}
			var p map[string]any
			_ = json.Unmarshal(e.Payload, &p)
			f.mu.Lock()
			f.sales = append(f.sales, p)
			f.mu.Unlock()
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// client builds a real cloudlink.Client dialled at this fake cloud, run for
// the test's duration and stopped on cleanup.
func (f *fakeCloudLink) client(t *testing.T) *cloudlink.Client {
	t.Helper()
	c := cloudlink.New(cloudlink.Options{
		Target: func(context.Context) (cloudlink.Target, bool) {
			return cloudlink.Target{BaseURL: f.srv.URL + "/api", StoreID: "store-1", Bearer: "cred-1", DeviceID: "dev-1"}, true
		},
		Kick:     func() {},
		Status:   func(context.Context) cloudlink.Status { return cloudlink.Status{} },
		Version:  "v-test",
		Platform: "test",
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	return c
}

func (f *fakeCloudLink) snapshot() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.sales))
	copy(out, f.sales)
	return out
}

// waitConnected blocks until the fake cloud has accepted the socket and
// sent its hello, or fails the test.
func (f *fakeCloudLink) waitConnected(t *testing.T) {
	t.Helper()
	select {
	case <-f.connected:
	case <-time.After(3 * time.Second):
		t.Fatal("the till never dialled the cloud link")
	}
}

// waitReady blocks until c is actually able to put a sale frame on the
// wire (linked AND live_view on) by probing externally with a throwaway
// sale — cloudlink.Client exposes no state outside its own package's
// _test.go files. Only meaningful when this fake's live_view is on.
func (f *fakeCloudLink) waitReady(t *testing.T, c *cloudlink.Client) {
	t.Helper()
	const probeID = "warmup-probe"
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.Sale(cloudlink.Sale{ID: probeID, Time: time.Now(), Currency: "GBP"})
		time.Sleep(5 * time.Millisecond)
		for _, s := range f.snapshot() {
			if s["id"] == probeID {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("cloud link never reached a ready (linked, live_view on) state")
		}
	}
}

// waitForSaleWithID blocks for a sale frame carrying id, or fails after a
// bound.
func (f *fakeCloudLink) waitForSaleWithID(t *testing.T, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		for _, s := range f.snapshot() {
			if s["id"] == id {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no sale frame with id %q reached the cloud", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// countSalesWithID returns how many sale frames carrying id have arrived
// so far.
func (f *fakeCloudLink) countSalesWithID(id string) int {
	n := 0
	for _, s := range f.snapshot() {
		if s["id"] == id {
			n++
		}
	}
	return n
}

// expectNoSaleWithID asserts no sale frame carrying id arrives within the
// window.
func (f *fakeCloudLink) expectNoSaleWithID(t *testing.T, id string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if f.countSalesWithID(id) > 0 {
			t.Fatalf("unexpected sale frame for id %q", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
