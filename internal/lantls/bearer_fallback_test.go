package lantls

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ut-docs#5203: a 401 from the main till for the replica's current bearer is
// retried once with the previous bearer (a main till restored from a backup
// that predates the last rotation knows only that one).

// authServer answers 200 for the bearers in ok and 401 otherwise, recording
// every request.
type authServer struct {
	*httptest.Server
	mu     sync.Mutex
	ok     map[string]bool
	seen   []string // "METHOD path bearer body"
	rotate atomic.Bool
}

func newAuthServer(t *testing.T, okBearers ...string) *authServer {
	t.Helper()
	a := &authServer{ok: map[string]bool{}}
	for _, b := range okBearers {
		a.ok[b] = true
	}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		a.mu.Lock()
		a.seen = append(a.seen, r.Method+" "+r.URL.Path+" "+b+" "+string(body))
		good := a.ok[b]
		a.mu.Unlock()
		if !good {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if a.rotate.Load() {
			w.Header().Set(BearerRotateHeader, "1")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *authServer) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seen...)
}

type fallbackHook struct {
	calls   atomic.Int32
	adopts  atomic.Int32
	prev    string
	ok      bool
	presend atomic.Value
	adopted atomic.Bool // what adopt reports
}

func installFallback(t *testing.T, prev string, ok bool) *fallbackHook {
	t.Helper()
	h := &fallbackHook{prev: prev, ok: ok}
	h.adopted.Store(true)
	t.Cleanup(SetBearerFallback(func(_ context.Context, presented string) (string, func() bool, bool) {
		h.calls.Add(1)
		h.presend.Store(presented)
		return h.prev, func() bool { h.adopts.Add(1); return h.adopted.Load() }, h.ok
	}))
	return h
}

func do(t *testing.T, method, url, bearer string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := NewPeerClient(5 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestBearerFallback_RetriesOnceWithPreviousAndAdopts(t *testing.T) {
	srv := newAuthServer(t, "prev")
	usePeer(t, srv.URL, "")
	h := installFallback(t, "prev", true)

	resp := do(t, http.MethodPost, srv.URL+"/api/sync/sales", "cur", strings.NewReader("payload"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the retry", resp.StatusCode)
	}
	got := srv.requests()
	want := []string{"POST /api/sync/sales cur payload", "POST /api/sync/sales prev payload"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("requests = %q, want %q (body replayed)", got, want)
	}
	if h.presend.Load() != "cur" {
		t.Fatalf("hook asked about %v, want cur", h.presend.Load())
	}
	if h.adopts.Load() != 1 {
		t.Fatalf("adopt called %d times, want 1", h.adopts.Load())
	}
}

func TestBearerFallback_AdoptRequestsImmediateRotation(t *testing.T) {
	srv := newAuthServer(t, "prev")
	usePeer(t, srv.URL, "")
	installFallback(t, "prev", true)
	rot := installRotateHook(t)
	bearerRotateRetryEvery = time.Hour // the throttle must not hold the forced one back
	// A rotation fired a moment ago:
	bearerRotateLast.Store(time.Now().UnixNano())

	if resp := do(t, http.MethodGet, srv.URL+"/api/sync/admin", "cur", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	rot.waitCall(t)
	close(rot.release)
}

func TestBearerFallback_RetryAlso401NoAdopt(t *testing.T) {
	srv := newAuthServer(t) // the main till expired the previous bearer too
	usePeer(t, srv.URL, "")
	h := installFallback(t, "prev", true)
	rot := installRotateHook(t)

	resp := do(t, http.MethodGet, srv.URL+"/api/sync/admin", "cur", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want the retry's 401", resp.StatusCode)
	}
	if n := len(srv.requests()); n != 2 {
		t.Fatalf("%d requests, want exactly 2 (one retry, no loop)", n)
	}
	if h.adopts.Load() != 0 {
		t.Fatal("adopted a previous bearer the main till refused")
	}
	rot.expectNoCall(t, "no adoption happened")
}

func TestBearerFallback_NotRetried(t *testing.T) {
	t.Run("hook says not ok", func(t *testing.T) {
		srv := newAuthServer(t, "prev")
		usePeer(t, srv.URL, "")
		h := installFallback(t, "prev", false)
		if resp := do(t, http.MethodGet, srv.URL+"/x", "cur", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if n := len(srv.requests()); n != 1 || h.adopts.Load() != 0 {
			t.Fatalf("requests=%d adopts=%d", n, h.adopts.Load())
		}
	})
	t.Run("no hook", func(t *testing.T) {
		srv := newAuthServer(t, "prev")
		usePeer(t, srv.URL, "")
		if resp := do(t, http.MethodGet, srv.URL+"/x", "cur", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if n := len(srv.requests()); n != 1 {
			t.Fatalf("requests = %d", n)
		}
	})
	t.Run("other host", func(t *testing.T) {
		main := newAuthServer(t, "prev")
		other := newAuthServer(t, "prev")
		usePeer(t, main.URL, "")
		h := installFallback(t, "prev", true)
		if resp := do(t, http.MethodGet, other.URL+"/x", "cur", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if n := len(other.requests()); n != 1 || h.calls.Load() != 0 {
			t.Fatalf("requests=%d hook calls=%d", n, h.calls.Load())
		}
	})
	t.Run("rotate path", func(t *testing.T) {
		srv := newAuthServer(t, "prev")
		usePeer(t, srv.URL, "")
		h := installFallback(t, "prev", true)
		if resp := do(t, http.MethodPost, srv.URL+BearerRotatePath, "cur", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if n := len(srv.requests()); n != 1 || h.calls.Load() != 0 {
			t.Fatalf("requests=%d hook calls=%d", n, h.calls.Load())
		}
	})
	t.Run("non-401", func(t *testing.T) {
		srv := newAuthServer(t, "cur", "prev")
		usePeer(t, srv.URL, "")
		h := installFallback(t, "prev", true)
		if resp := do(t, http.MethodGet, srv.URL+"/x", "cur", nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if h.calls.Load() != 0 {
			t.Fatal("hook consulted for a 200")
		}
	})
	t.Run("no bearer on the request", func(t *testing.T) {
		srv := newAuthServer(t, "prev")
		usePeer(t, srv.URL, "")
		h := installFallback(t, "prev", true)
		if resp := do(t, http.MethodGet, srv.URL+"/x", "", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if h.calls.Load() != 0 {
			t.Fatal("hook consulted for a request with no bearer")
		}
	})
	t.Run("non-replayable body", func(t *testing.T) {
		srv := newAuthServer(t, "prev")
		usePeer(t, srv.URL, "")
		h := installFallback(t, "prev", true)
		// An opaque reader gives http.NewRequest no GetBody.
		body := io.NopCloser(strings.NewReader("payload"))
		if resp := do(t, http.MethodPost, srv.URL+"/x", "cur", body); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if n := len(srv.requests()); n != 1 || h.adopts.Load() != 0 {
			t.Fatalf("requests=%d adopts=%d", n, h.adopts.Load())
		}
	})
}

func TestBearerFallback_PinnedMainTill(t *testing.T) {
	main := newPeerTill(t)
	usePeer(t, main.base, main.cert.Pin())
	// newPeerTill answers 200 to everything; make it demand a bearer.
	main.requireBearer.Store("prev")
	h := installFallback(t, "prev", true)

	resp := do(t, http.MethodGet, main.base+"/api/sync/admin", "cur", nil)
	if resp.StatusCode != http.StatusOK || h.adopts.Load() != 1 {
		t.Fatalf("status=%d adopts=%d, want 200 and 1", resp.StatusCode, h.adopts.Load())
	}
	if main.plain.Load() != 0 || main.tls.Load() != 2 {
		t.Fatalf("plain=%d tls=%d, want the retry over pinned TLS too", main.plain.Load(), main.tls.Load())
	}
}

// An adopt that reports false (it lost the CAS to a concurrent adoption or a
// re-join) must not trigger an extra unthrottled rotation.
func TestBearerFallback_AdoptNotPerformedNoForcedRotation(t *testing.T) {
	srv := newAuthServer(t, "prev")
	usePeer(t, srv.URL, "")
	h := installFallback(t, "prev", true)
	h.adopted.Store(false)
	rot := installRotateHook(t)
	bearerRotateRetryEvery = time.Hour
	bearerRotateLast.Store(time.Now().UnixNano())

	if resp := do(t, http.MethodGet, srv.URL+"/api/sync/admin", "cur", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if h.adopts.Load() != 1 {
		t.Fatalf("adopt called %d times, want 1", h.adopts.Load())
	}
	rot.expectNoCall(t, "adopt reported it did not switch bearers")
}

// A forced rotation requested while one is already in flight is not lost: the
// running one is followed by exactly one more.
func TestFireBearerRotate_ForcedWhileBusyRunsAgainOnce(t *testing.T) {
	rot := installRotateHook(t)
	bearerRotateRetryEvery = time.Hour

	fireBearerRotateOpt(false)
	rot.waitCall(t) // in flight, blocked
	fireBearerRotateOpt(true)
	fireBearerRotateOpt(true) // two forced requests while busy fold into one extra run
	fireBearerRotateOpt(false)
	close(rot.release)
	rot.waitCall(t)
	rot.expectNoCall(t, "only one extra run per busy period")
	waitUntil(t, func() bool { return !bearerRotateBusy.Load() })
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("timed out")
}

// The 401 is drained and closed before the retry goes out, so the retry
// reuses the keep-alive connection instead of opening a second one.
func TestBearerFallback_RetryReusesConnection(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer prev" {
			http.Error(w, strings.Repeat("no ", 100), http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	usePeer(t, srv.URL, "")
	installFallback(t, "prev", true)

	if resp := do(t, http.MethodGet, srv.URL+"/api/sync/admin", "cur", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if n := conns.Load(); n != 1 {
		t.Fatalf("%d connections, want 1 (the retry should reuse the keep-alive one)", n)
	}
}

// When the retry gets no answer at all the first 401 stands, body intact.
func TestBearerFallback_RetryTransportErrorKeepsFirstBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer prev" {
			c, _, _ := w.(http.Hijacker).Hijack()
			_ = c.Close()
			return
		}
		http.Error(w, "first-body", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	usePeer(t, srv.URL, "")
	h := installFallback(t, "prev", true)

	resp := do(t, http.MethodGet, srv.URL+"/api/sync/admin", "cur", nil)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(b), "first-body") {
		t.Fatalf("status=%d body=%q, want the first 401 with its body", resp.StatusCode, b)
	}
	if h.adopts.Load() != 0 {
		t.Fatal("adopted after a failed retry")
	}
}
