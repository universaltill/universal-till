package fleetlink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fastConfig shrinks every ADR-0114 timing so the heartbeat/backpressure
// tests run in milliseconds; the limits are the real ones unless a test
// overrides them.
func fastConfig() Config {
	c := DefaultConfig()
	c.PingInterval = 50 * time.Millisecond
	c.PeerTimeout = 2 * time.Second // heartbeat tests shrink it themselves
	c.WriteTimeout = 150 * time.Millisecond
	c.RequestTimeout = 500 * time.Millisecond
	return c
}

// fakeConn is an in-memory Conn: the test feeds reads through in, sees
// writes on out, and can hold every write on gate to simulate a peer that
// is not reading.
type fakeConn struct {
	in     chan []byte
	out    chan []byte
	gate   chan struct{} // nil = writes pass immediately
	mu     sync.Mutex
	closed bool
	code   CloseCode
	done   chan struct{}
}

func newFakeConn() *fakeConn {
	return &fakeConn{in: make(chan []byte, 64), out: make(chan []byte, 1024), done: make(chan struct{})}
}

func (f *fakeConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case b := <-f.in:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.done:
		return nil, errors.New("closed")
	}
}

func (f *fakeConn) Write(ctx context.Context, b []byte) error {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return ctx.Err()
		case <-f.done:
			return errors.New("closed")
		}
	}
	select {
	case f.out <- append([]byte(nil), b...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeConn) Close(code CloseCode, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed, f.code = true, code
		close(f.done)
	}
}

func (f *fakeConn) closeCode() (CloseCode, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.code, f.closed
}

// next returns the next written envelope, skipping pings.
func (f *fakeConn) next(t *testing.T) Envelope {
	t.Helper()
	for {
		select {
		case b := <-f.out:
			e, err := Decode(b)
			if err != nil {
				t.Fatalf("server wrote an undecodable frame %s: %v", b, err)
			}
			if e.Type == TypePing {
				continue
			}
			return e
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for a frame")
		}
	}
}

func (f *fakeConn) send(t *testing.T, e Envelope) {
	t.Helper()
	b, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	f.in <- b
}

func mustMsg(t *testing.T, id, typ, replyTo string, payload any) Envelope {
	t.Helper()
	e, err := newMessage(id, typ, replyTo, payload)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// testHello is the Hello hook the hub tests install.
func testHello(_ context.Context, tillID string) Hello {
	return Hello{TillID: "main-1", Role: "main", Version: "v9.9.9", Platform: "linux/arm64",
		SyncProtocol: SyncProtocolLevel, Cursors: Cursors{Admin: "a1", Plugins: "p1", Stock: "s1"}, PeerTillID: tillID}
}

// wsHarness serves hub over a real httptest server with a stand-in for
// syncTill: "Bearer good-<id>" authenticates as till <id>.
type wsHarness struct {
	hub *Hub
	srv *httptest.Server
}

func newWSHarness(t *testing.T, cfg Config) *wsHarness {
	t.Helper()
	hub := NewHub(HubOptions{Config: cfg, Hello: testHello})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		id, ok := strings.CutPrefix(tok, "good-")
		if !ok || id == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		hub.Serve(w, r, id)
	}))
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
	})
	return &wsHarness{hub: hub, srv: srv}
}

func (h *wsHarness) dial(t *testing.T, bearer string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	hdr := http.Header{}
	if bearer != "" {
		hdr.Set("Authorization", "Bearer "+bearer)
	}
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.srv.URL, "http"), &websocket.DialOptions{HTTPHeader: hdr})
	if c != nil {
		c.SetReadLimit(1 << 20)
		t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, resp, err
}

// readEnv reads the next non-ping envelope from a real client.
func readEnv(t *testing.T, c *websocket.Conn) Envelope {
	t.Helper()
	for {
		e, err := readEnvErr(c, 2*time.Second)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if e.Type == TypePing {
			continue
		}
		return e
	}
}

func readEnvErr(c *websocket.Conn, d time.Duration) (Envelope, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		return Envelope{}, err
	}
	return Decode(b)
}

func writeEnv(t *testing.T, c *websocket.Conn, e Envelope) {
	t.Helper()
	b, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// waitFor polls cond until true or fails after 2s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func decodeInto[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}
