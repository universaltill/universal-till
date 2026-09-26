package cloudlink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/universaltill/universal-till/internal/fleetlink"
)

// fakeCloud stands in for ut-cloud's GET /api/v1/tills/link
// (internal/tilllink): it records every upgrade request, can refuse one
// with a status, and otherwise sends the cloud hello and hands the socket
// to the test.
type fakeCloud struct {
	t   *testing.T
	srv *httptest.Server

	linkVersion atomic.Int64
	liveView    atomic.Bool

	mu     sync.Mutex
	dials  []*http.Request
	refuse []refusal // consumed one per dial, front first

	conns chan *cloudConn
}

type refusal struct {
	status     int
	retryAfter int // seconds; 0 = no header
}

type cloudConn struct {
	ws     *websocket.Conn
	frames chan fleetlink.Envelope
	closed chan struct{}
}

func newFakeCloud(t *testing.T) *fakeCloud {
	f := &fakeCloud{t: t, conns: make(chan *cloudConn, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/tills/link", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.dials = append(f.dials, r.Clone(context.Background()))
		var ref *refusal
		if len(f.refuse) > 0 {
			ref = &f.refuse[0]
			f.refuse = f.refuse[1:]
		}
		f.mu.Unlock()
		if ref != nil {
			if ref.retryAfter > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(ref.retryAfter))
			}
			w.WriteHeader(ref.status)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c := &cloudConn{ws: ws, frames: make(chan fleetlink.Envelope, 64), closed: make(chan struct{})}
		c.send("hello", map[string]any{"link_version": f.linkVersion.Load(), "live_view": f.liveView.Load()})
		f.conns <- c
		defer close(c.closed)
		for {
			_, b, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			env, err := fleetlink.Decode(b)
			if err != nil {
				continue
			}
			if env.Type == "ping" {
				continue
			}
			select {
			case c.frames <- env:
			default:
			}
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCloud) dialCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.dials)
}

func (f *fakeCloud) queueRefusal(r refusal) {
	f.mu.Lock()
	f.refuse = append(f.refuse, r)
	f.mu.Unlock()
}

func (f *fakeCloud) nextConn(t *testing.T) *cloudConn {
	t.Helper()
	select {
	case c := <-f.conns:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("the till never opened the cloud link")
		return nil
	}
}

func (c *cloudConn) send(typ string, payload any) {
	raw, _ := json.Marshal(payload)
	b, _ := fleetlink.Encode(fleetlink.Envelope{ID: "c-" + typ + strconv.FormatInt(time.Now().UnixNano(), 36), Type: typ, Payload: raw})
	_ = c.ws.Write(context.Background(), websocket.MessageText, b)
}

func (c *cloudConn) closeWith(code int, reason string) {
	_ = c.ws.Close(websocket.StatusCode(code), reason)
}

// frame waits for the next frame of type typ from the till.
func (c *cloudConn) frame(t *testing.T, typ string) fleetlink.Envelope {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case env := <-c.frames:
			if env.Type == typ {
				return env
			}
		case <-deadline:
			t.Fatalf("no %q frame from the till", typ)
			return fleetlink.Envelope{}
		}
	}
}

// noFrame asserts no frame of type typ arrives within d.
func (c *cloudConn) noFrame(t *testing.T, typ string, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case env := <-c.frames:
			if env.Type == typ {
				t.Fatalf("unexpected %q frame: %s", typ, env.Payload)
			}
		case <-deadline:
			return
		}
	}
}

// harness runs a Client against a fakeCloud with test timings.
type harness struct {
	cloud  *fakeCloud
	c      *Client
	kicks  atomic.Int32
	gateOK atomic.Bool
	bearer atomic.Value // string
	status atomic.Value // Status
	cancel context.CancelFunc
	done   chan struct{}
}

func fastOptions() Options {
	cfg := DefaultConfig()
	cfg.PingInterval = 50 * time.Millisecond
	cfg.PeerTimeout = 2 * time.Second
	cfg.WriteTimeout = 200 * time.Millisecond
	return Options{
		Config:       cfg,
		RecheckEvery: 20 * time.Millisecond,
		RedialSpread: 20 * time.Millisecond,
		BackoffMin:   5 * time.Millisecond,
		BackoffMax:   20 * time.Millisecond,
		DialTimeout:  time.Second,
		StatusEvery:  30 * time.Millisecond,
	}
}

func newHarness(t *testing.T, mod func(*Options)) *harness {
	t.Helper()
	h := &harness{cloud: newFakeCloud(t), done: make(chan struct{})}
	h.gateOK.Store(true)
	h.bearer.Store("cred-1")
	h.status.Store(Status{Version: "v1.2.3", UpdateState: "idle"})
	o := fastOptions()
	o.Target = func(context.Context) (Target, bool) {
		return Target{BaseURL: h.cloud.srv.URL + "/api", StoreID: "store-1", Bearer: h.bearer.Load().(string), DeviceID: "dev-1"}, h.gateOK.Load()
	}
	o.Version, o.Platform = "v1.2.3", "linux/arm64"
	o.Kick = func() { h.kicks.Add(1) }
	o.Status = func(context.Context) Status { return h.status.Load().(Status) }
	if mod != nil {
		mod(&o)
	}
	h.c = New(o)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { defer close(h.done); h.c.Run(ctx) }()
	t.Cleanup(h.stop)
	return h
}

func (h *harness) stop() {
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		panic("cloudlink Client.Run did not return after cancel")
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
