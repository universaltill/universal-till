package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// buildTCPGuest compiles the wasip1 TCP test guest once per test run.
func buildTCPGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "tcp_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/tcp_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 tcp guest: %v\n%s", err, raw)
	}
	return out
}

// startTCPFixture listens on a real loopback socket — NOT a mock — and runs
// handler on every accepted connection. Returns host, port, an accept
// counter, and a channel delivering the accepted conns to the test.
func startTCPFixture(t *testing.T, handler func(net.Conn)) (string, int, *atomic.Int32, <-chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	var accepts atomic.Int32
	conns := make(chan net.Conn, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			conns <- conn
			if handler != nil {
				go handler(conn)
			}
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, &accepts, conns
}

// runTCPGuest fires one event carrying the scenario payload through the real
// wasm module and returns the results the guest recorded in plugin storage.
func runTCPGuest(t *testing.T, w *WasmRuntime, d *sql.DB, pluginID string, payload map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(payload)
	ev := Event{ID: "ev1", Type: "test.tcp", Timestamp: time.Now(), Payload: raw}
	w.mu.Lock()
	w.db = d
	w.hasTCP[pluginID] = true // widen the deadline as Sync would for tcp: grants
	w.mu.Unlock()
	if _, err := w.HandleEvent(context.Background(), pluginID, ev); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	return readGuestResults(t, d, pluginID)
}

func readGuestResults(t *testing.T, d *sql.DB, pluginID string) map[string]any {
	t.Helper()
	raw, err := data.NewPluginRepo(d).StorageGet(context.Background(), pluginID, "results")
	if err != nil {
		t.Fatalf("read results: %v", err)
	}
	var results map[string]any
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatalf("parse results %s: %v", raw, err)
	}
	return results
}

func newTCPTestRuntime(t *testing.T, guest, pluginID string) *WasmRuntime {
	t.Helper()
	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	// tcpConns is a package-level registry (it must outlive per-event module
	// instances), so a test's handles and handle-counter otherwise leak into
	// the NEXT run of the same test under -count=2/-shuffle (review finding
	// N5) since every test here reuses the same literal pluginID. Reset this
	// plugin's slice of the shared registry on both ends of the test.
	tcpConns.CloseAll(pluginID)
	t.Cleanup(func() { tcpConns.CloseAll(pluginID) })
	return w
}

// Without any tcp: grant, tcp_open must return -2 (denied) and the fixture
// server must never see a connection.
func TestHostTCPOpenDeniedWithoutPermission(t *testing.T) {
	guest := buildTCPGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.tcpdenied"
	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")       // results only; no tcp: grant
	grantPerm(t, d, pluginID, "net:127.0.0.1") // net: must NOT satisfy tcp:

	host, port, accepts, _ := startTCPFixture(t, nil)
	w := newTCPTestRuntime(t, guest, pluginID)

	res := runTCPGuest(t, w, d, pluginID, map[string]any{
		"mode": "roundtrip", "host": host, "port": port,
		"connect_timeout_ms": 1000, "read_timeout_ms": 1000, "send": "x",
	})
	if res["open_code"] != float64(hostErrDenied) {
		t.Fatalf("open_code = %v, want %d (denied)", res["open_code"], hostErrDenied)
	}
	if n := accepts.Load(); n != 0 {
		t.Fatalf("device saw %d connection(s) without a tcp: grant", n)
	}
}

// Exact tcp:<host>:<port> grant: full open/write/read/close round trip with
// real bytes over a real socket, close idempotent.
func TestHostTCPExactGrantRoundTrip(t *testing.T) {
	guest := buildTCPGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.tcpexact"

	host, port, _, _ := startTCPFixture(t, func(conn net.Conn) {
		defer conn.Close()
		buf := make([]byte, 1024)
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		_, _ = conn.Write(append([]byte("ack:"), buf[:n]...))
	})

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, fmt.Sprintf("tcp:%s:%d", host, port))

	w := newTCPTestRuntime(t, guest, pluginID)
	res := runTCPGuest(t, w, d, pluginID, map[string]any{
		"mode": "roundtrip", "host": host, "port": port,
		"connect_timeout_ms": 2000, "read_timeout_ms": 2000, "send": "hello-zvt",
	})

	if res["open_code"] != float64(0) {
		t.Fatalf("open_code = %v, want 0 (first handle)", res["open_code"])
	}
	if res["write_code"] != float64(len("hello-zvt")) {
		t.Errorf("write_code = %v, want %d", res["write_code"], len("hello-zvt"))
	}
	if res["read_data"] != "ack:hello-zvt" {
		t.Errorf("read_data = %q, want %q (read_code=%v)", res["read_data"], "ack:hello-zvt", res["read_code"])
	}
	if res["close_code"] != float64(0) || res["close_again_code"] != float64(0) {
		t.Errorf("close not idempotent: close=%v again=%v", res["close_code"], res["close_again_code"])
	}
}

// The tcp:* wildcard (review-gated, same convention as net:*) authorises any
// host:port.
func TestHostTCPWildcardGrant(t *testing.T) {
	guest := buildTCPGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.tcpwild"

	host, port, _, _ := startTCPFixture(t, func(conn net.Conn) {
		defer conn.Close()
		buf := make([]byte, 64)
		if n, err := conn.Read(buf); err == nil {
			_, _ = conn.Write(buf[:n])
		}
	})

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, "tcp:*") // wildcard, NOT the exact host:port

	w := newTCPTestRuntime(t, guest, pluginID)
	res := runTCPGuest(t, w, d, pluginID, map[string]any{
		"mode": "roundtrip", "host": host, "port": port,
		"connect_timeout_ms": 2000, "read_timeout_ms": 2000, "send": "ping",
	})
	if res["open_code"] != float64(0) {
		t.Fatalf("tcp:* did not authorise the open; open_code = %v", res["open_code"])
	}
	if res["read_data"] != "ping" {
		t.Errorf("echo round trip failed: read_data = %q", res["read_data"])
	}
}

// Dialing a dead port (listener closed before the call) fails fast with -3 —
// a broken terminal must never hang the till.
func TestHostTCPConnectFailure(t *testing.T) {
	guest := buildTCPGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.tcpdead"

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing is listening on this port anymore

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, fmt.Sprintf("tcp:127.0.0.1:%d", port))

	w := newTCPTestRuntime(t, guest, pluginID)
	start := time.Now()
	res := runTCPGuest(t, w, d, pluginID, map[string]any{
		"mode": "roundtrip", "host": "127.0.0.1", "port": port,
		"connect_timeout_ms": 500, "read_timeout_ms": 500, "send": "x",
	})
	if res["open_code"] != float64(hostErrInternal) {
		t.Fatalf("open_code = %v, want %d (internal)", res["open_code"], hostErrInternal)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("connect failure took %v — the timeout is not bounding the dial", elapsed)
	}
}

// A device that accepts but never answers: the read comes back -3 after the
// guest's own read deadline instead of hanging until the event deadline.
func TestHostTCPReadTimeout(t *testing.T) {
	guest := buildTCPGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.tcpsilent"

	host, port, _, _ := startTCPFixture(t, nil) // accepts, never writes

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, fmt.Sprintf("tcp:%s:%d", host, port))

	w := newTCPTestRuntime(t, guest, pluginID)
	start := time.Now()
	res := runTCPGuest(t, w, d, pluginID, map[string]any{
		"mode": "readtimeout", "host": host, "port": port,
		"connect_timeout_ms": 2000, "read_timeout_ms": 200,
	})
	if res["read_code"] != float64(hostErrInternal) {
		t.Fatalf("read_code = %v, want %d (timeout → internal)", res["read_code"], hostErrInternal)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("silent read took %v — the read deadline is not applied", elapsed)
	}
}

// A plugin holds at most 4 concurrent handles: the fifth open returns -4.
// Handles are sequential per plugin starting at 0.
func TestHostTCPMaxHandles(t *testing.T) {
	guest := buildTCPGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.tcpmax"

	host, port, _, _ := startTCPFixture(t, nil)

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, fmt.Sprintf("tcp:%s:%d", host, port))

	w := newTCPTestRuntime(t, guest, pluginID)
	res := runTCPGuest(t, w, d, pluginID, map[string]any{
		"mode": "maxhandles", "host": host, "port": port,
		"connect_timeout_ms": 2000,
	})
	rawCodes, ok := res["open_codes"].([]any)
	if !ok || len(rawCodes) != 5 {
		t.Fatalf("open_codes = %v, want 5 entries", res["open_codes"])
	}
	for i := 0; i < 4; i++ {
		if rawCodes[i] != float64(i) {
			t.Errorf("open #%d = %v, want sequential handle %d", i+1, rawCodes[i], i)
		}
	}
	if rawCodes[4] != float64(hostErrInvalid) {
		t.Errorf("fifth open = %v, want %d (max handles exceeded)", rawCodes[4], hostErrInvalid)
	}
}

// A disabled/removed plugin never leaks an open socket: Sync dropping the
// module must force-close every registry handle. Asserted on the REAL device
// side — the fixture's accepted net.Conn reads EOF after the Sync.
func TestHostTCPCloseAllOnPluginUnload(t *testing.T) {
	guest := buildTCPGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.tcpunload"

	host, port, _, conns := startTCPFixture(t, nil)

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, fmt.Sprintf("tcp:%s:%d", host, port))

	w := newTCPTestRuntime(t, guest, pluginID)
	res := runTCPGuest(t, w, d, pluginID, map[string]any{
		"mode": "openonly", "host": host, "port": port,
		"connect_timeout_ms": 2000,
	})
	if res["open_code"] != float64(0) {
		t.Fatalf("open_code = %v, want 0", res["open_code"])
	}

	var deviceConn net.Conn
	select {
	case deviceConn = <-conns:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture never received the connection")
	}
	defer deviceConn.Close()

	// The handle survives the event (the registry outlives the instance).
	if _, ok := tcpConns.Get(pluginID, 0); !ok {
		t.Fatal("open handle not in the registry after the event")
	}

	// Deactivate the plugin and Sync: the module is dropped and CloseAll
	// must fire at the same point hasNet/hasTCP bookkeeping is cleared.
	if _, err := d.Exec(`UPDATE plugins SET is_active = 0 WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	w.Sync(context.Background(), d)
	defer SharedBus(d).ResetSubscribers()

	if _, ok := tcpConns.Get(pluginID, 0); ok {
		t.Error("registry still holds the handle after unload")
	}
	_ = deviceConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := deviceConn.Read(buf); err == nil {
		t.Error("device-side read succeeded — the socket was not closed on unload")
	}
}

// Sync populates hasTCP from granted tcp: permissions the same way it does
// hasNet from net:, and timeoutFor widens the deadline for either.
func TestWasmRuntimeTCPTimeoutWidening(t *testing.T) {
	w := NewWasmRuntime(t.TempDir())
	const pluginID = "com.test.tcptimeout"

	if got := w.timeoutFor(pluginID, "payment.zvt.authorize"); got != 2*time.Second {
		t.Fatalf("default timeout = %v, want 2s", got)
	}
	w.mu.Lock()
	w.hasTCP[pluginID] = true
	w.mu.Unlock()
	if got := w.timeoutFor(pluginID, "payment.zvt.authorize"); got != 10*time.Second {
		t.Fatalf("tcp-permitted timeout = %v, want netTimeout 10s", got)
	}
}

func TestPluginHasTCPPermission(t *testing.T) {
	d := hostfnTestDB(t)
	ctx := context.Background()

	const withTCP = "com.test.hastcp"
	seedPlugin(t, d, withTCP)
	grantPerm(t, d, withTCP, "tcp:192.168.1.50:20007")
	if !pluginHasTCPPermission(ctx, d, withTCP) {
		t.Error("granted tcp:<host>:<port> not detected")
	}

	const netOnly = "com.test.netonly"
	seedPlugin(t, d, netOnly)
	grantPerm(t, d, netOnly, "net:api.example.com")
	if pluginHasTCPPermission(ctx, d, netOnly) {
		t.Error("net: grant must not count as tcp:")
	}
}

// Registry unit semantics: per-plugin sequential handles, isolation between
// plugins, idempotent Close, CloseAll clears everything for one plugin only.
func TestTCPConnRegistry(t *testing.T) {
	r := newTCPConnRegistry()
	fixtureConn := func() net.Conn {
		c1, c2 := net.Pipe()
		t.Cleanup(func() { c1.Close(); c2.Close() })
		return c1
	}

	hA0, ok := r.Open("plugin.a", fixtureConn())
	if !ok || hA0 != 0 {
		t.Fatalf("first handle for plugin.a = %d,%v, want 0,true", hA0, ok)
	}
	hA1, _ := r.Open("plugin.a", fixtureConn())
	if hA1 != 1 {
		t.Fatalf("second handle = %d, want sequential 1", hA1)
	}
	hB0, _ := r.Open("plugin.b", fixtureConn())
	if hB0 != 0 {
		t.Fatalf("plugin.b first handle = %d, want its own sequence starting 0", hB0)
	}

	if _, ok := r.Get("plugin.a", hA0); !ok {
		t.Error("Get lost an open handle")
	}
	if _, ok := r.Get("plugin.a", 99); ok {
		t.Error("Get returned an unknown handle")
	}
	if _, ok := r.Get("plugin.b", hA1); ok {
		t.Error("plugin.b sees plugin.a's handle")
	}

	r.Close("plugin.a", hA0)
	if _, ok := r.Get("plugin.a", hA0); ok {
		t.Error("handle still present after Close")
	}
	r.Close("plugin.a", hA0) // idempotent: closing again must not panic

	// Handles are never reused within a plugin's lifetime.
	hA2, _ := r.Open("plugin.a", fixtureConn())
	if hA2 != 2 {
		t.Errorf("handle after close = %d, want 2 (no reuse)", hA2)
	}

	r.CloseAll("plugin.a")
	if _, ok := r.Get("plugin.a", hA1); ok {
		t.Error("CloseAll left plugin.a handles behind")
	}
	if _, ok := r.Get("plugin.b", hB0); !ok {
		t.Error("CloseAll(plugin.a) closed plugin.b's handle")
	}
	r.CloseAll("plugin.b")
}

// Max concurrent handles per plugin is 4 at the registry level too.
func TestTCPConnRegistryMaxHandles(t *testing.T) {
	r := newTCPConnRegistry()
	for i := 0; i < maxTCPHandlesPerPlugin; i++ {
		c1, c2 := net.Pipe()
		t.Cleanup(func() { c1.Close(); c2.Close() })
		if _, ok := r.Open("plugin.max", c1); !ok {
			t.Fatalf("open #%d refused below the cap", i+1)
		}
	}
	c1, c2 := net.Pipe()
	t.Cleanup(func() { c1.Close(); c2.Close() })
	if _, ok := r.Open("plugin.max", c1); ok {
		t.Fatal("open beyond the per-plugin cap succeeded")
	}
	r.CloseAll("plugin.max")
}

// Review finding N1 (ut-docs#542): a plain fmt.Sprintf("%s:%d", host, port)
// breaks on any host containing a colon -- every IPv6 literal. tcpAddr must
// bracket correctly (net.JoinHostPort's job), and must leave IPv4/hostnames
// unchanged since existing permission grants and docs use the bare form.
func TestTCPAddr(t *testing.T) {
	cases := []struct {
		host string
		port uint32
		want string
	}{
		{"127.0.0.1", 20007, "127.0.0.1:20007"},
		{"terminal.local", 8080, "terminal.local:8080"},
		{"::1", 20007, "[::1]:20007"},
		{"fe80::1", 20007, "[fe80::1]:20007"},
		{"2001:db8::5", 443, "[2001:db8::5]:443"},
	}
	for _, c := range cases {
		if got := tcpAddr(c.host, c.port); got != c.want {
			t.Errorf("tcpAddr(%q, %d) = %q, want %q", c.host, c.port, got, c.want)
		}
	}
}

// tcp timeouts are clamped to sane bounds so a plugin can neither pass 0
// (block forever semantics) nor an hour.
func TestClampTCPTimeout(t *testing.T) {
	if got := clampTCPTimeoutMs(0, maxTCPDialTimeoutMs); got != 1*time.Millisecond {
		t.Errorf("clamp(0) = %v, want 1ms floor", got)
	}
	if got := clampTCPTimeoutMs(500, maxTCPDialTimeoutMs); got != 500*time.Millisecond {
		t.Errorf("clamp(500) = %v, want 500ms", got)
	}
	if got := clampTCPTimeoutMs(999999, maxTCPDialTimeoutMs); got != 15*time.Second {
		t.Errorf("dial clamp(999999) = %v, want 15s cap", got)
	}
	if got := clampTCPTimeoutMs(999999, maxTCPReadTimeoutMs); got != 30*time.Second {
		t.Errorf("read clamp(999999) = %v, want 30s cap", got)
	}
}

// Review finding B1 (ut-docs#542): a guest-requested read timeout LONGER
// than the plugin's own EVENT deadline must not let the host call outlive
// the event. HandleEvent's Blocking events are dispatched synchronously
// from the cashier's tender request (a payment.*.authorize call reaches
// here) — a device that never answers must freeze checkout for at most
// the event's own deadline, never for whatever the guest happened to ask
// tcp_read for. Silent fixture (accepts, never writes) + a read timeout
// (25s) deliberately longer than the tcp:-widened event deadline (10s)
// reproduces exactly what the independent review measured pre-fix:
// HandleEvent took ~25.03s. With effectiveDeadline in place it must
// return bounded by the event deadline instead (module killed at ctx
// expiry is an expected, not a failing, outcome here).
func TestHostTCPReadDoesNotOutliveEventDeadline(t *testing.T) {
	guest := buildTCPGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.tcpdeadline"

	host, port, _, _ := startTCPFixture(t, nil) // accepts, never writes

	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, fmt.Sprintf("tcp:%s:%d", host, port))

	w := newTCPTestRuntime(t, guest, pluginID)
	w.mu.Lock()
	w.db = d
	w.hasTCP[pluginID] = true // event deadline widens to w.netTimeout
	eventDeadline := w.netTimeout
	w.mu.Unlock()

	payload, err := json.Marshal(map[string]any{
		"mode": "readtimeout", "host": host, "port": port,
		"connect_timeout_ms": 2000,
		"read_timeout_ms":    25000, // longer than eventDeadline on purpose
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	ev := Event{ID: "ev1", Type: "test.tcp", Timestamp: time.Now(), Payload: payload}

	start := time.Now()
	// Any error here (e.g. the module being killed at the event deadline)
	// is an expected outcome of the fix, not a test failure -- the only
	// thing this test asserts is elapsed wall-clock time.
	_, _ = w.HandleEvent(context.Background(), pluginID, ev)
	elapsed := time.Since(start)

	slack := 3 * time.Second // process/CI overhead only, nowhere near 25s
	if elapsed > eventDeadline+slack {
		t.Fatalf("HandleEvent took %v for a guest read_timeout_ms of 25000, want bounded by the %v event deadline (+%v slack) -- B1 regression", elapsed, eventDeadline, slack)
	}
}
