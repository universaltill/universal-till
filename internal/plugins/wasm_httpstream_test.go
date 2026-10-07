package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/netaccess"
)

// ADR-0121 §3 (ut-docs#3156): http:stream — http_open / http_write /
// http_status / http_read / http_close. Driven through a real wasip1 guest
// (testdata/httpstream_guest) against real loopback HTTP servers.

func buildHTTPStreamGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "httpstream_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/httpstream_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 httpstream guest: %v\n%s", err, raw)
	}
	return out
}

// streamFixture is the loopback server every stream test talks to.
type streamFixture struct {
	srv       *httptest.Server
	held      chan struct{} // /hold: closed when the client went away
	heldOnce  atomic.Bool
	release   chan struct{} // /stall: closed at test end
	stallHits atomic.Int32
	hits      atomic.Int32
}

func newStreamFixture(t *testing.T) *streamFixture {
	t.Helper()
	f := &streamFixture{held: make(chan struct{}), release: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/ndjson", func(w http.ResponseWriter, _ *http.Request) {
		f.hits.Add(1)
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "{\"i\":%d,\"msg\":\"chunk %d\"}\n", i, i)
			fl.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusCreated)
		if len(b) > 64 {
			fmt.Fprintf(w, "%s %d", r.Method, len(b))
			return
		}
		fmt.Fprintf(w, "%s %d %s", r.Method, len(b), b)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		_, _ = w.Write([]byte(strings.Repeat("b", n)))
	})
	mux.HandleFunc("/hold", func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		_, _ = w.Write([]byte("first\n"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			if f.heldOnce.CompareAndSwap(false, true) {
				close(f.held)
			}
		case <-f.release:
		}
	})
	mux.HandleFunc("/stall", func(w http.ResponseWriter, r *http.Request) {
		f.stallHits.Add(1)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-f.release:
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		close(f.release)
		f.srv.Close()
	})
	return f
}

func (f *streamFixture) url(t *testing.T, path string) string {
	return "http://localhost:" + mustPort(t, f.srv.URL) + path
}

// newStreamRuntime compiles nothing: guest is built once per test.
func newStreamRuntime(t *testing.T, guest, pluginID string, env *egressEnv) *WasmRuntime {
	t.Helper()
	w := NewWasmRuntime(t.TempDir())
	w.httpClient = env.client(nil)
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.hasNet[pluginID] = true
	return w
}

func streamDB(t *testing.T, pluginID string, perms ...string) *sql.DB {
	t.Helper()
	d := hostfnTestDB(t)
	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	for _, p := range perms {
		grantPerm(t, d, pluginID, p)
	}
	return d
}

func codes(v any) []int {
	arr, _ := v.([]any)
	out := make([]int, 0, len(arr))
	for _, x := range arr {
		f, _ := x.(float64)
		out = append(out, int(f))
	}
	return out
}

func lastCode(v any) int {
	c := codes(v)
	if len(c) == 0 {
		return -999
	}
	return c[len(c)-1]
}

var streamLoopEnv = &egressEnv{dns: map[string][]string{"localhost": {"127.0.0.1"}}}

func TestHTTPStreamNDJSONAcrossReads(t *testing.T) {
	guest := buildHTTPStreamGuest(t)
	f := newStreamFixture(t)
	const pluginID = "com.test.stream.ndjson"
	w := newStreamRuntime(t, guest, pluginID, streamLoopEnv)
	d := streamDB(t, pluginID, "net:localhost", "http:stream")

	res := runGuestPayload(t, w, d, pluginID, map[string]any{"url": f.url(t, "/ndjson"), "read_cap": 16})
	if res["open_code"] != float64(0) {
		t.Fatalf("open_code = %v, want 0", res["open_code"])
	}
	var st struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal([]byte(fmt.Sprint(res["status_json"])), &st); err != nil {
		t.Fatalf("status_json %v: %v", res["status_json"], err)
	}
	if st.Status != 200 || st.Headers["Content-Type"] != "application/x-ndjson" {
		t.Fatalf("status = %+v", st)
	}
	if res["status_again"] != res["status_code"] {
		t.Fatalf("second http_status = %v, first %v", res["status_again"], res["status_code"])
	}
	var want strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&want, "{\"i\":%d,\"msg\":\"chunk %d\"}\n", i, i)
	}
	if res["read_data"] != want.String() {
		t.Fatalf("read_data = %q, want %q", res["read_data"], want.String())
	}
	// 16-byte reads of a ~125-byte body: many calls, the last one EOF (0).
	if n := res["read_count"].(float64); n < 5 {
		t.Fatalf("read_count = %v, want several reads", n)
	}
	if lastCode(res["read_codes"]) != 0 {
		t.Fatalf("read_codes = %v, want trailing 0 (EOF)", res["read_codes"])
	}
	for _, c := range codes(res["read_codes"]) {
		if c > 16 {
			t.Fatalf("a read returned %d > dstCap 16", c)
		}
	}
	if res["late_write_code"] != float64(hostErrInvalid) {
		t.Fatalf("http_write after send = %v, want %d", res["late_write_code"], hostErrInvalid)
	}
	if res["close_code"] != float64(0) || res["close_again_code"] != float64(hostErrNotFound) {
		t.Fatalf("close = %v, again = %v; want 0, %d", res["close_code"], res["close_again_code"], hostErrNotFound)
	}
}

func TestHTTPStreamRequestBody(t *testing.T) {
	guest := buildHTTPStreamGuest(t)
	f := newStreamFixture(t)
	const pluginID = "com.test.stream.body"
	w := newStreamRuntime(t, guest, pluginID, streamLoopEnv)
	d := streamDB(t, pluginID, "net:localhost", "http:stream")

	res := runGuestPayload(t, w, d, pluginID, map[string]any{
		"url": f.url(t, "/echo"), "method": "POST", "body": "hello stream", "write_chunk": 5,
	})
	if got := codes(res["write_codes"]); fmt.Sprint(got) != "[5 5 2]" {
		t.Fatalf("write_codes = %v, want [5 5 2]", got)
	}
	if res["read_data"] != "POST 12 hello stream" {
		t.Fatalf("read_data = %q", res["read_data"])
	}
	if !strings.Contains(fmt.Sprint(res["status_json"]), `"status":201`) || !strings.Contains(fmt.Sprint(res["status_json"]), `"X-Test":"yes"`) {
		t.Fatalf("status_json = %v", res["status_json"])
	}
}

const oneMiB = 1 << 20

func limitManifest(pluginID string, mb int) string {
	return fmt.Sprintf(`{"id":%q,"name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm","limits":{"http_body_mb":%d}}`, pluginID, mb)
}

func TestHTTPStreamResponseCap(t *testing.T) {
	guest := buildHTTPStreamGuest(t)
	f := newStreamFixture(t)
	const pluginID = "com.test.stream.respcap"
	withInstalledManifest(t, pluginID, limitManifest(pluginID, 1))
	w := newStreamRuntime(t, guest, pluginID, streamLoopEnv)

	t.Run("exactly the limit is read whole", func(t *testing.T) {
		d := streamDB(t, pluginID, "net:localhost", "http:stream")
		res := runGuestPayload(t, w, d, pluginID, map[string]any{"url": f.url(t, "/big?n="+strconv.Itoa(oneMiB))})
		if res["read_total"] != float64(oneMiB) || lastCode(res["read_codes"]) != 0 {
			t.Fatalf("read_total = %v, codes %v; want %d then EOF", res["read_total"], res["read_codes"], oneMiB)
		}
	})
	t.Run("one byte over → quota exceeded, handle closed", func(t *testing.T) {
		d := streamDB(t, pluginID, "net:localhost", "http:stream")
		res := runGuestPayload(t, w, d, pluginID, map[string]any{"url": f.url(t, "/big?n="+strconv.Itoa(oneMiB+1))})
		if lastCode(res["read_codes"]) != hostErrQuota {
			t.Fatalf("read_codes = %v, want trailing %d", res["read_codes"], hostErrQuota)
		}
		if tot := res["read_total"].(float64); tot > oneMiB {
			t.Fatalf("guest received %v bytes, more than the %d limit", tot, oneMiB)
		}
		if res["close_code"] != float64(hostErrNotFound) {
			t.Fatalf("close after quota = %v, want %d (already closed)", res["close_code"], hostErrNotFound)
		}
	})
}

func TestHTTPStreamRequestCap(t *testing.T) {
	guest := buildHTTPStreamGuest(t)
	f := newStreamFixture(t)
	const pluginID = "com.test.stream.reqcap"
	withInstalledManifest(t, pluginID, limitManifest(pluginID, 1))
	w := newStreamRuntime(t, guest, pluginID, streamLoopEnv)

	t.Run("exactly the limit is sent", func(t *testing.T) {
		d := streamDB(t, pluginID, "net:localhost", "http:stream")
		res := runGuestPayload(t, w, d, pluginID, map[string]any{"url": f.url(t, "/echo"), "method": "POST", "body_len": oneMiB})
		if res["read_data"] != "POST "+strconv.Itoa(oneMiB) {
			t.Fatalf("read_data = %q (write codes %v)", res["read_data"], res["write_codes"])
		}
	})
	t.Run("one byte over → quota exceeded, nothing sent", func(t *testing.T) {
		d := streamDB(t, pluginID, "net:localhost", "http:stream")
		before := f.hits.Load()
		res := runGuestPayload(t, w, d, pluginID, map[string]any{"url": f.url(t, "/echo"), "method": "POST", "body_len": oneMiB + 1})
		if lastCode(res["write_codes"]) != hostErrQuota {
			t.Fatalf("write_codes = %v, want trailing %d", res["write_codes"], hostErrQuota)
		}
		if res["status_code"] != float64(hostErrNotFound) {
			t.Fatalf("status after quota = %v, want %d (handle closed)", res["status_code"], hostErrNotFound)
		}
		if f.hits.Load() != before {
			t.Fatal("an over-quota request body was still sent")
		}
	})
}

func TestHTTPStreamMaxHandles(t *testing.T) {
	guest := buildHTTPStreamGuest(t)
	f := newStreamFixture(t)
	const pluginID = "com.test.stream.max"
	w := newStreamRuntime(t, guest, pluginID, streamLoopEnv)
	d := streamDB(t, pluginID, "net:localhost", "http:stream")
	res := runGuestPayload(t, w, d, pluginID, map[string]any{"mode": "maxhandles", "url": f.url(t, "/ndjson")})
	if got := fmt.Sprint(codes(res["open_codes"])); got != fmt.Sprintf("[0 1 2 3 %d]", hostErrBusy) {
		t.Fatalf("open_codes = %s, want [0 1 2 3 %d]", got, hostErrBusy)
	}
	if rc := res["reopen_code"].(float64); rc < 0 {
		t.Fatalf("reopen after close = %v, want a handle", rc)
	}
}

func TestHTTPStreamDenials(t *testing.T) {
	guest := buildHTTPStreamGuest(t)
	f := newStreamFixture(t)
	var lanHits, metaHits atomic.Int32
	lan := httptest.NewServer(hitCounter(&lanHits, "lan"))
	defer lan.Close()
	meta := httptest.NewServer(hitCounter(&metaHits, "meta"))
	defer meta.Close()
	env := &egressEnv{
		dns: map[string][]string{"localhost": {"127.0.0.1"}, "lan.example.com": {"192.168.1.20"}, "public.example.com": {"93.184.216.34"}},
		route: map[string]string{
			"192.168.1.20": lan.Listener.Addr().String(), "93.184.216.34": lan.Listener.Addr().String(),
			"169.254.169.254": meta.Listener.Addr().String(),
		},
	}
	const pluginID = "com.test.stream.deny"
	w := newStreamRuntime(t, guest, pluginID, env)

	cases := []struct {
		name       string
		perms      []string
		url        string
		wantOpen   int32 // when < 0
		wantStatus int32 // when wantOpen == 0: the http_status code
		noHit      *atomic.Int32
	}{
		{name: "missing http:stream", perms: []string{"net:localhost"}, url: f.url(t, "/ndjson"), wantOpen: hostErrDenied},
		{name: "http:stream without a net grant", perms: []string{"http:stream"}, url: f.url(t, "/ndjson"), wantOpen: hostErrDenied},
		{name: "plain http to a LAN host without http:lan", perms: []string{"http:stream", "net:lan.example.com"}, url: "http://lan.example.com/x", wantOpen: hostErrInvalid, noHit: &lanHits},
		{name: "plain http to a LAN host with http:lan", perms: []string{"http:stream", "net:lan.example.com", "http:lan"}, url: "http://lan.example.com/x", wantStatus: 0},
		{name: "plain http under http:lan to a public IP refused at send", perms: []string{"http:stream", "net:public.example.com", "http:lan"}, url: "http://public.example.com/x", wantStatus: hostErrDenied, noHit: &lanHits},
		{name: "metadata refused with exact grant + http:lan", perms: []string{"http:stream", "net:169.254.169.254", "http:lan"}, url: "http://169.254.169.254/latest", wantStatus: hostErrDenied, noHit: &metaHits},
		{name: "unsupported scheme", perms: []string{"http:stream", "net:*"}, url: "ftp://lan.example.com/x", wantOpen: hostErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := streamDB(t, pluginID, c.perms...)
			var before int32
			if c.noHit != nil {
				before = c.noHit.Load()
			}
			res := runGuestPayload(t, w, d, pluginID, map[string]any{"url": c.url})
			if c.wantOpen < 0 {
				if res["open_code"] != float64(c.wantOpen) {
					t.Fatalf("open_code = %v, want %d", res["open_code"], c.wantOpen)
				}
			} else {
				if res["open_code"] != float64(0) {
					t.Fatalf("open_code = %v, want 0", res["open_code"])
				}
				sc := res["status_code"].(float64)
				if c.wantStatus < 0 && sc != float64(c.wantStatus) {
					t.Fatalf("status_code = %v, want %d", sc, c.wantStatus)
				}
				if c.wantStatus == 0 && sc <= 0 {
					t.Fatalf("status_code = %v, want a status JSON length", sc)
				}
			}
			if c.noHit != nil && c.noHit.Load() != before {
				t.Fatal("refused stream still reached the target server")
			}
		})
	}

	t.Run("demo mode", func(t *testing.T) {
		netaccess.SetDemo(true)
		t.Cleanup(func() { netaccess.SetDemo(false) })
		d := streamDB(t, pluginID, "net:localhost", "http:stream")
		res := runGuestPayload(t, w, d, pluginID, map[string]any{"url": f.url(t, "/ndjson")})
		if res["open_code"] != float64(hostErrDenied) {
			t.Fatalf("open_code = %v, want %d", res["open_code"], hostErrDenied)
		}
	})

	t.Run("unknown handle", func(t *testing.T) {
		d := streamDB(t, pluginID, "http:stream")
		res := runGuestPayload(t, w, d, pluginID, map[string]any{"mode": "badhandle"})
		for _, k := range []string{"read_code", "status_code", "write_code", "close_code"} {
			if res[k] != float64(hostErrNotFound) {
				t.Errorf("%s = %v, want %d", k, res[k], hostErrNotFound)
			}
		}
	})
}

// A guest that returns with a handle still open: the host closes it when the
// event returns, so the server sees its client go away. The first chunk
// arriving while the server is still mid-body also proves the response is
// streamed, not buffered whole.
func TestHTTPStreamHandlesClosedWhenEventReturns(t *testing.T) {
	guest := buildHTTPStreamGuest(t)
	f := newStreamFixture(t)
	const pluginID = "com.test.stream.leak"
	w := newStreamRuntime(t, guest, pluginID, streamLoopEnv)
	d := streamDB(t, pluginID, "net:localhost", "http:stream")
	var states []*hostState
	onHostState = func(s *hostState) { states = append(states, s) }
	t.Cleanup(func() { onHostState = nil })
	res := runGuestPayload(t, w, d, pluginID, map[string]any{"mode": "leak", "url": f.url(t, "/hold")})
	if len(states) != 1 {
		t.Fatalf("saw %d host states, want 1", len(states))
	}
	if n := states[0].streams.count(); n != 0 {
		t.Fatalf("%d stream handles still open after the event returned", n)
	}
	if states[0].streams.next != 1 {
		t.Fatalf("guest opened %d handles, want 1 (the leak scenario did not run)", states[0].streams.next)
	}
	if res["read_data"] != "first\n" {
		t.Fatalf("read_data = %q (open %v, status %v, read %v)", res["read_data"], res["open_code"], res["status_code"], res["read_code"])
	}
	select {
	case <-f.held:
	case <-time.After(5 * time.Second):
		t.Fatal("server never saw the connection close after the event returned")
	}
}

func TestHTTPStreamIdleTimeout(t *testing.T) {
	guest := buildHTTPStreamGuest(t)
	f := newStreamFixture(t)
	prev := httpStreamIdleTimeout
	httpStreamIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { httpStreamIdleTimeout = prev })
	const pluginID = "com.test.stream.idle"
	w := newStreamRuntime(t, guest, pluginID, streamLoopEnv)
	d := streamDB(t, pluginID, "net:localhost", "http:stream")
	start := time.Now()
	res := runGuestPayload(t, w, d, pluginID, map[string]any{"url": f.url(t, "/stall")})
	elapsed := time.Since(start)
	if c := codes(res["read_codes"]); len(c) != 1 || c[0] != hostErrInternal {
		t.Fatalf("read_codes = %v, want [%d]", res["read_codes"], hostErrInternal)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("stalled read took %v with a 300ms idle timeout", elapsed)
	}
}

func TestHTTPStreamEventDeadlineUnblocksRead(t *testing.T) {
	guest := buildHTTPStreamGuest(t)
	f := newStreamFixture(t)
	const pluginID = "com.test.stream.deadline"
	w := newStreamRuntime(t, guest, pluginID, streamLoopEnv)
	w.netTimeout = time.Second
	d := streamDB(t, pluginID, "net:localhost", "http:stream")
	w.mu.Lock()
	w.db = d
	w.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"url": f.url(t, "/stall")})
	start := time.Now()
	// The guest may get -3 or be killed at the deadline; only time matters.
	_, _ = w.HandleEvent(context.Background(), pluginID, Event{ID: "e", Type: "test.event", Timestamp: time.Now(), Payload: raw})
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("stalled stream read outlived the 1s event deadline: %v (30s idle timeout)", elapsed)
	}
	if f.stallHits.Load() != 1 {
		t.Fatalf("stall server hits = %d, want 1", f.stallHits.Load())
	}
}

func TestHTTPStreamRegistry(t *testing.T) {
	r := &httpStreams{}
	var cancelled atomic.Int32
	for i := 0; i < maxHTTPStreamHandles; i++ {
		h, ok := r.add(&httpStream{cancel: func() { cancelled.Add(1) }})
		if !ok || h != int32(i) {
			t.Fatalf("add %d = %d, %v", i, h, ok)
		}
	}
	if _, ok := r.add(&httpStream{cancel: func() {}}); ok {
		t.Fatal("a 5th handle was admitted")
	}
	if _, ok := r.get(2); !ok {
		t.Fatal("get(2) missing")
	}
	r.close(2)
	if _, ok := r.get(2); ok {
		t.Fatal("closed handle still found")
	}
	if cancelled.Load() != 1 {
		t.Fatalf("close cancelled %d streams, want 1", cancelled.Load())
	}
	r.closeAll()
	if n := r.count(); n != 0 {
		t.Fatalf("after closeAll %d handles remain", n)
	}
	if cancelled.Load() != maxHTTPStreamHandles {
		t.Fatalf("cancelled %d, want %d", cancelled.Load(), maxHTTPStreamHandles)
	}
}

func TestHTTPStreamLimitBytes(t *testing.T) {
	cases := []struct {
		m    *Manifest
		goos string
		want int64
	}{
		{nil, "linux", 8 << 20},
		{&Manifest{}, "linux", 8 << 20},
		{&Manifest{Limits: &ManifestLimits{HTTPBodyMB: 2}}, "linux", 2 << 20},
		{&Manifest{Limits: &ManifestLimits{HTTPBodyMB: 100}}, "linux", 64 << 20},
		{&Manifest{Limits: &ManifestLimits{HTTPBodyMB: 100}}, "android", 32 << 20},
	}
	for _, c := range cases {
		if got := httpStreamLimitBytes(c.m, c.goos); got != c.want {
			t.Errorf("httpStreamLimitBytes(%+v, %s) = %d, want %d", c.m, c.goos, got, c.want)
		}
	}
}
