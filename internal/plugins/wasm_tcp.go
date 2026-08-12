package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Raw TCP transport for local-hardware/device plugins (ut-docs#542,
// ADR-0001 amendment 2026-08-12): a wasm plugin holding a granted
// `tcp:<host>:<port>` permission (or the review-gated `tcp:*` wildcard, same
// convention as net:*) may speak a raw protocol — e.g. ZVT to a LAN payment
// terminal — through the tcp_open/tcp_write/tcp_read/tcp_close host
// functions. Every operation is deadline-bound: a dead or silent device
// returns an error code to the plugin, it never hangs checkout.
//
// Connections are tracked in a per-plugin handle registry that OUTLIVES the
// per-event module instance (a payment flow spans several events), and are
// force-closed by WasmRuntime.Sync when the plugin is disabled, removed or
// reloaded, so a dropped plugin never leaks a socket.

const (
	// maxTCPHandlesPerPlugin caps concurrent open sockets per plugin — a
	// device plugin talks to one terminal, a handful at most.
	maxTCPHandlesPerPlugin = 4
	// maxTCPDialTimeoutMs / maxTCPReadTimeoutMs bound the per-call deadlines
	// a guest may request (floor is 1ms — 0 must not mean "forever").
	maxTCPDialTimeoutMs = 15000
	maxTCPReadTimeoutMs = 30000
	// tcpWriteTimeout is the fixed deadline for tcp_write: writes to a LAN
	// device buffer locally and either complete fast or the link is broken.
	tcpWriteTimeout = 10 * time.Second
	// tcpReadBufCap bounds the host-side read buffer regardless of the
	// dstCap the guest claims (mirrors httpResponseCap's role).
	tcpReadBufCap = 256 << 10
)

// clampTCPTimeoutMs converts a guest-supplied timeout to a duration bounded
// to [1ms, maxMs].
func clampTCPTimeoutMs(ms, maxMs uint32) time.Duration {
	if ms < 1 {
		ms = 1
	}
	if ms > maxMs {
		ms = maxMs
	}
	return time.Duration(ms) * time.Millisecond
}

// tcpConnRegistry tracks open device connections by (pluginID, handle).
// Handles are sequential per plugin starting at 0 and never reused within a
// plugin's registry lifetime, so a stale handle can't alias a new socket.
type tcpConnRegistry struct {
	mu       sync.Mutex
	byPlugin map[string]map[int32]net.Conn
	next     map[string]int32
}

func newTCPConnRegistry() *tcpConnRegistry {
	return &tcpConnRegistry{
		byPlugin: map[string]map[int32]net.Conn{},
		next:     map[string]int32{},
	}
}

// tcpConns is the process-wide registry: like sharedBus, connections must
// survive per-event module instances, and Sync must be able to reap them.
var tcpConns = newTCPConnRegistry()

// Open registers conn under the plugin's next handle. Returns (handle, true)
// or (0, false) when the plugin is already at maxTCPHandlesPerPlugin — the
// caller still owns (and must close) conn in that case.
func (r *tcpConnRegistry) Open(pluginID string, conn net.Conn) (int32, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.byPlugin[pluginID]) >= maxTCPHandlesPerPlugin {
		return 0, false
	}
	if r.byPlugin[pluginID] == nil {
		r.byPlugin[pluginID] = map[int32]net.Conn{}
	}
	h := r.next[pluginID]
	r.next[pluginID] = h + 1
	r.byPlugin[pluginID][h] = conn
	return h, true
}

// Get looks up an open connection by (pluginID, handle).
func (r *tcpConnRegistry) Get(pluginID string, handle int32) (net.Conn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	conn, ok := r.byPlugin[pluginID][handle]
	return conn, ok
}

// Close closes and removes one handle; unknown handles are a no-op
// (tcp_close is idempotent).
func (r *tcpConnRegistry) Close(pluginID string, handle int32) {
	r.mu.Lock()
	conn, ok := r.byPlugin[pluginID][handle]
	if ok {
		delete(r.byPlugin[pluginID], handle)
	}
	r.mu.Unlock()
	if ok {
		_ = conn.Close()
	}
}

// CloseAll force-closes every handle a plugin holds and clears its entry.
// Called from WasmRuntime.Sync when a plugin's module is dropped, so a
// disabled/removed/reloaded plugin never leaks an open socket.
func (r *tcpConnRegistry) CloseAll(pluginID string) {
	r.mu.Lock()
	conns := r.byPlugin[pluginID]
	delete(r.byPlugin, pluginID)
	delete(r.next, pluginID)
	r.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

// hostTCPOpen dials a raw TCP connection to <host>:<port>. The pair must be
// covered by a granted `tcp:<host>:<port>` permission or the `tcp:*`
// wildcard (exact-then-wildcard, same as hostHTTPRequest). timeoutMs is
// clamped to [1, 15000]. Returns the plugin-scoped handle (≥ 0) or a
// hostErr* code.
func hostTCPOpen(ctx context.Context, m api.Module, hostPtr, hostLen, port, timeoutMs uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	hostRaw, ok := readGuest(m, hostPtr, hostLen)
	if !ok || len(hostRaw) == 0 {
		return hostErrInvalid
	}
	host := string(hostRaw)
	if port == 0 || port > 65535 {
		return hostErrInvalid
	}
	// Grant the call when the plugin holds either the exact host:port
	// permission (tcp:<host>:<port>) or the wildcard (tcp:*). Configurable
	// terminal plugins learn their device address from install-time settings,
	// so they declare tcp:* and are review-gated accordingly (like net:*).
	if err := CheckPermission(ctx, s.db, s.pluginID, fmt.Sprintf("tcp:%s:%d", host, port)); err != nil {
		if err2 := CheckPermission(ctx, s.db, s.pluginID, "tcp:*"); err2 != nil {
			return hostErrDenied
		}
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), clampTCPTimeoutMs(timeoutMs, maxTCPDialTimeoutMs))
	if err != nil {
		logging.L().Infof("[wasm:%s] tcp open %s:%d failed: %v", s.pluginID, host, port, err)
		return hostErrInternal
	}
	handle, ok := tcpConns.Open(s.pluginID, conn)
	if !ok {
		_ = conn.Close()
		logging.L().Infof("[wasm:%s] tcp open %s:%d refused: max %d handles", s.pluginID, host, port, maxTCPHandlesPerPlugin)
		return hostErrInvalid
	}
	return handle
}

// hostTCPWrite writes the guest buffer to the connection under a fixed 10s
// write deadline. Returns bytes written or a hostErr* code.
func hostTCPWrite(ctx context.Context, m api.Module, handle int32, ptr, length uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	conn, ok := tcpConns.Get(s.pluginID, handle)
	if !ok {
		return hostErrNotFound
	}
	buf, ok := readGuest(m, ptr, length)
	if !ok {
		return hostErrInvalid
	}
	_ = conn.SetWriteDeadline(time.Now().Add(tcpWriteTimeout))
	n, err := conn.Write(buf)
	if err != nil {
		logging.L().Infof("[wasm:%s] tcp write handle %d failed: %v", s.pluginID, handle, err)
		return hostErrInternal
	}
	return int32(n)
}

// hostTCPRead performs ONE Read (protocols frame their own messages — no
// fixed-length assumption) under a deadline clamped to [1, 30000] ms and
// writes what arrived into the guest buffer (full-length-return semantics
// per the buffer ABI). Timeout or I/O error → hostErrInternal.
func hostTCPRead(ctx context.Context, m api.Module, handle int32, dstPtr, dstCap, timeoutMs uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	conn, ok := tcpConns.Get(s.pluginID, handle)
	if !ok {
		return hostErrNotFound
	}
	if dstCap == 0 {
		return hostErrInvalid
	}
	bufCap := dstCap
	if bufCap > tcpReadBufCap {
		bufCap = tcpReadBufCap
	}
	_ = conn.SetReadDeadline(time.Now().Add(clampTCPTimeoutMs(timeoutMs, maxTCPReadTimeoutMs)))
	buf := make([]byte, bufCap)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		logging.L().Infof("[wasm:%s] tcp read handle %d failed: %v", s.pluginID, handle, err)
		return hostErrInternal
	}
	return writeGuest(m, dstPtr, dstCap, buf[:n])
}

// hostTCPClose closes and forgets the handle. Idempotent: closing an unknown
// or already-closed handle also returns 0.
func hostTCPClose(ctx context.Context, handle int32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	tcpConns.Close(s.pluginID, handle)
	return 0
}

// pluginHasTCPPermission reports whether any granted permission is tcp:*,
// which widens the module's event deadline for device round-trips (same
// mechanism as pluginHasNetPermission for net:).
func pluginHasTCPPermission(ctx context.Context, db *sql.DB, pluginID string) bool {
	perms, err := data.NewPluginRepo(db).ListPermissions(ctx, pluginID)
	if err != nil {
		return false
	}
	for _, p := range perms {
		if p.Granted && strings.HasPrefix(p.Permission, "tcp:") {
			return true
		}
	}
	return false
}
