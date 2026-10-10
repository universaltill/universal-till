package plugins

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A self-hosted model answering a non-streamed request sends its headers
// only when generation ends (ut-docs#4035): an ordinary event still gives
// up after pluginHeaderWait, a job waits up to its own deadline.
func TestHTTPRequestHeaderWaitFollowsJobDeadline(t *testing.T) {
	guest := buildHostfnGuest(t)
	prev := pluginHeaderWait
	pluginHeaderWait = 300 * time.Millisecond // stands in for 30 s
	t.Cleanup(func() { pluginHeaderWait = prev })
	const slow = 1200 * time.Millisecond // stands in for a 40 s generation

	env := &egressEnv{dns: map[string][]string{"localhost": {"127.0.0.1"}}}
	// Each subtest gets its own server, which reports how long the client
	// waited before giving up (or slow, when it answered): the event's own
	// wall time includes compiling the guest, so it can't tell a header
	// wait from a slow start.
	slowServer := func(t *testing.T) (string, <-chan time.Duration) {
		waited := make(chan time.Duration, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			select {
			case <-time.After(slow):
			case <-r.Context().Done():
				waited <- time.Since(start)
				return
			}
			waited <- slow
			_, _ = w.Write([]byte("generated"))
		}))
		t.Cleanup(srv.Close)
		return "http://localhost:" + mustPort(t, srv.URL) + "/api/chat", waited
	}

	run := func(t *testing.T, ctx context.Context, pluginID, url string) map[string]any {
		t.Helper()
		d := streamDB(t, pluginID, "net:localhost")
		w := NewWasmRuntime(t.TempDir())
		w.httpClient = env.client(nil)
		if err := w.load(pluginID, "1.0.0", guest); err != nil {
			t.Fatalf("load: %v", err)
		}
		w.hasNet[pluginID] = true
		return runGuestPayloadCtx(ctx, t, w, d, pluginID, map[string]string{"url": url})
	}

	t.Run("ordinary event refused after the header wait", func(t *testing.T) {
		url, waited := slowServer(t)
		res := run(t, context.Background(), "com.test.headerwait.event", url)
		if res["http_code"] != float64(hostErrInternal) {
			t.Fatalf("http_code = %v (status %v), want %d", res["http_code"], res["http_status"], hostErrInternal)
		}
		if el := <-waited; el >= slow {
			t.Fatalf("ordinary event waited %v for headers, want about %v", el, pluginHeaderWait)
		}
	})
	t.Run("job waits for the headers within its deadline", func(t *testing.T) {
		ctx := WithJob(context.Background(), JobCall{Deadline: 120 * time.Second})
		url, waited := slowServer(t)
		res := run(t, ctx, "com.test.headerwait.job", url)
		if res["http_status"] != float64(200) {
			t.Fatalf("http_status = %v (code %v), want 200", res["http_status"], res["http_code"])
		}
		if el := <-waited; el != slow {
			t.Fatalf("server was abandoned after %v", el)
		}
	})
	t.Run("http_open waits in a job, not in an ordinary event", func(t *testing.T) {
		sguest := buildHTTPStreamGuest(t)
		for _, c := range []struct {
			name string
			ctx  context.Context
			ok   bool
		}{
			{"event", context.Background(), false},
			{"job", WithJob(context.Background(), JobCall{Deadline: 120 * time.Second}), true},
		} {
			url, waited := slowServer(t)
			pluginID := "com.test.headerwait.stream." + c.name
			w := newStreamRuntime(t, sguest, pluginID, env)
			d := streamDB(t, pluginID, "net:localhost", "http:stream")
			res := runGuestPayloadCtx(c.ctx, t, w, d, pluginID, map[string]any{"url": url})
			sc, _ := res["status_code"].(float64)
			if c.ok && (sc <= 0 || !strings.Contains(fmt.Sprint(res["status_json"]), `"status":200`)) {
				t.Fatalf("%s: status_code %v, status_json %v, want 200", c.name, res["status_code"], res["status_json"])
			}
			if !c.ok && sc != float64(hostErrInternal) {
				t.Fatalf("%s: status_code %v, want %d", c.name, res["status_code"], hostErrInternal)
			}
			if el := <-waited; (el == slow) != c.ok {
				t.Fatalf("%s: server waited %v", c.name, el)
			}
		}
	})
	t.Run("job still bounded by its own deadline", func(t *testing.T) {
		ctx := WithJob(context.Background(), JobCall{Deadline: 600 * time.Millisecond})
		url, waited := slowServer(t)
		_, _ = runGuestHandle(ctx, t, guest, env, "com.test.headerwait.short", url)
		select {
		case el := <-waited:
			if el >= slow {
				t.Fatalf("job with a 600ms deadline waited %v for headers", el)
			}
		case <-time.After(5 * time.Second):
			// The guest hit its deadline before sending (slow instantiation):
			// still bounded, which is what this subtest checks.
			t.Log("request never reached the server within the 600ms deadline")
		}
	})
}

// runGuestHandle runs one event and returns HandleEvent's own result — for
// a call that may be killed at its deadline before writing results.
func runGuestHandle(ctx context.Context, t *testing.T, guest string, env *egressEnv, pluginID, url string) ([]byte, error) {
	t.Helper()
	d := streamDB(t, pluginID, "net:localhost")
	w := NewWasmRuntime(t.TempDir())
	w.httpClient = env.client(nil)
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true
	w.mu.Lock()
	w.db = d
	w.mu.Unlock()
	body := []byte(`{"url":` + strconv.Quote(url) + `}`)
	return w.HandleEvent(ctx, pluginID, Event{ID: "ev1", Type: "test.event", Timestamp: time.Now(), Payload: body})
}
