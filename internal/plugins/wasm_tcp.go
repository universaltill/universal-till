package plugins

import (
	"context"
	"database/sql"
	"net"
	"strconv"
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

// effectiveDeadline returns the earlier of "now + timeout" and ctx's own
// deadline, if it has one. Review finding B1 (ut-docs#542): a guest-supplied
// timeout alone can outlive the wasm event's own deadline (10s for a plugin
// holding tcp:/net:, per timeoutFor) — a blocked device call must never run
// longer than the event itself is allowed to, or a Blocking event (e.g. a
// payment.*.authorize tender call) freezes checkout for as long as the
// per-call timeout permits instead of the shorter event deadline.
func effectiveDeadline(ctx context.Context, timeout time.Duration) time.Time {
	d := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(d) {
		return ctxDeadline
	}
	return d
}

// tcpAddr formats a host:port pair via net.JoinHostPort so an IPv6 literal
// (e.g. "::1", "fe80::1") is bracketed correctly — a plain fmt.Sprintf
// "%s:%d" breaks on any host containing a colon (review finding N1).
func tcpAddr(host string, port uint32) string {
	return net.JoinHostPort(host, strconv.FormatUint(uint64(port), 10))
}

// tcpHandleInfo pairs an open connection with the exact `tcp:<host>:<port>`
// permission string that authorized it at Open time (ut-docs#606 item 1).
// Re-checking permission on every tcp_write/tcp_read needs this same string
// — not one re-derived from conn.RemoteAddr(), which can differ from the
// requested hostname after DNS resolution and would then check a grant that
// was never actually the one issued.
type tcpHandleInfo struct {
	conn net.Conn
	addr string
}

// tcpConnRegistry tracks open device connections by (pluginID, handle).
// Handles are sequential per plugin starting at 0 and never reused within
// one continuous registration lifetime — from a plugin's first Open until
// its next CloseAll — so a stale handle from earlier in that same lifetime
// can't alias a new socket. The guarantee does NOT span a CloseAll itself
// (ut-docs#606 item 3): a disable→re-enable cycle, or a version reload,
// restarts numbering at 0 for that plugin. This is a deliberately
// documented, not fixed, gap — see CloseAll's own comment for why it's
// academic.
type tcpConnRegistry struct {
	mu       sync.Mutex
	byPlugin map[string]map[int32]tcpHandleInfo
	next     map[string]int32
}

func newTCPConnRegistry() *tcpConnRegistry {
	return &tcpConnRegistry{
		byPlugin: map[string]map[int32]tcpHandleInfo{},
		next:     map[string]int32{},
	}
}

// tcpConns is the process-wide registry: like sharedBus, connections must
// survive per-event module instances, and Sync must be able to reap them.
var tcpConns = newTCPConnRegistry()

// Open registers conn under the plugin's next handle, recording addr (the
// `tcp:<host>:<port>` permission string checked to authorize this dial) for
// later per-call re-checks. Returns (handle, true) or (0, false) when the
// plugin is already at maxTCPHandlesPerPlugin — the caller still owns (and
// must close) conn in that case.
func (r *tcpConnRegistry) Open(pluginID string, conn net.Conn, addr string) (int32, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.byPlugin[pluginID]) >= maxTCPHandlesPerPlugin {
		return 0, false
	}
	if r.byPlugin[pluginID] == nil {
		r.byPlugin[pluginID] = map[int32]tcpHandleInfo{}
	}
	h := r.next[pluginID]
	r.next[pluginID] = h + 1
	r.byPlugin[pluginID][h] = tcpHandleInfo{conn: conn, addr: addr}
	return h, true
}

// Get looks up an open connection by (pluginID, handle).
func (r *tcpConnRegistry) Get(pluginID string, handle int32) (net.Conn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.byPlugin[pluginID][handle]
	return info.conn, ok
}

// GetWithAddr is Get plus the `tcp:<host>:<port>` permission string that was
// checked when the handle was opened, in a single locked lookup —
// hostTCPWrite/hostTCPRead need both the connection and its authorization
// address per call, and looking them up as two separately-locked calls
// leaves a re-lock window where a concurrent Close/CloseAll for the same
// handle could interleave between them (reviewed 2026-08-22, ut-docs#606
// review finding, non-blocking: analysis showed the window already fails
// closed or, in the narrowest cross-reload race, degrades to a spurious
// closed-connection I/O error rather than misdirecting a call onto a
// different plugin's connection — this closes the window regardless,
// since a single lock is free to have).
func (r *tcpConnRegistry) GetWithAddr(pluginID string, handle int32) (net.Conn, string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.byPlugin[pluginID][handle]
	return info.conn, info.addr, ok
}

// Close closes and removes one handle; unknown handles are a no-op
// (tcp_close is idempotent).
func (r *tcpConnRegistry) Close(pluginID string, handle int32) {
	r.mu.Lock()
	info, ok := r.byPlugin[pluginID][handle]
	if ok {
		delete(r.byPlugin[pluginID], handle)
	}
	r.mu.Unlock()
	if ok {
		_ = info.conn.Close()
	}
}

// CloseAll force-closes every handle a plugin holds and clears its entry,
// INCLUDING the handle counter (`next`) — a disable→re-enable cycle (or a
// version reload, ut-docs#606 item 2) restarts handle numbering at 0 for
// that plugin. Numbering is therefore only guaranteed monotonic within one
// registration lifetime, not across a CloseAll — see TestTCPConnRegistry's
// "no reuse" case, which asserts this within a lifetime, and
// TestTCPConnRegistryHandleNumberingAfterCloseAll, which asserts the reset
// across one. A plugin can only ever alias its own past connections this
// way (registries are per-plugin), so the impact is academic; `next` is
// int32 and unguarded against wraparound at 2^31 opens for the same
// reason — also academic given the 4-handle cap. Called from
// WasmRuntime.Sync when a plugin's module is dropped (disabled, removed,
// or updated to a new version), so a disabled/removed/reloaded plugin
// never leaks an open socket.
func (r *tcpConnRegistry) CloseAll(pluginID string) {
	r.mu.Lock()
	infos := r.byPlugin[pluginID]
	delete(r.byPlugin, pluginID)
	delete(r.next, pluginID)
	r.mu.Unlock()
	for _, info := range infos {
		_ = info.conn.Close()
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
	addr := tcpAddr(host, port)
	if !tcpAddrAuthorized(ctx, s, addr) {
		return hostErrDenied
	}
	// DialContext, not DialTimeout (review finding B1): the guest's own
	// timeout is clamped as before, but the dial must ALSO give up the
	// moment ctx's event deadline expires, or a dial that's about to time
	// out anyway can still outlive the event that's supposed to bound it.
	dialCtx, cancel := context.WithTimeout(ctx, clampTCPTimeoutMs(timeoutMs, maxTCPDialTimeoutMs))
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", tcpAddr(host, port))
	if err != nil {
		logging.L().Infof("[wasm:%s] tcp open %s:%d failed: %v", s.pluginID, host, port, err)
		return hostErrInternal
	}
	handle, ok := tcpConns.Open(s.pluginID, conn, addr)
	if !ok {
		_ = conn.Close()
		logging.L().Infof("[wasm:%s] tcp open %s:%d refused: max %d handles", s.pluginID, host, port, maxTCPHandlesPerPlugin)
		return hostErrInvalid
	}
	return handle
}

// tcpAddrAuthorized checks addr (a `tcp:<host>:<port>` permission string)
// against the plugin's current grants — exact address, else the `tcp:*`
// wildcard. It is called both at tcp_open and on every subsequent
// tcp_write/tcp_read for the SAME handle (ut-docs#606 item 1), so revoking a
// plugin's tcp: grant mid-flight denies its very next call on an
// already-open socket, matching wasm_hostfns.go's module-wide invariant
// ("every capability is permission-gated per call... denials are audited
// and grants are revocable live") that tcp_write/tcp_read previously
// violated by only authorizing once, at open time.
//
// The exact/wildcard probes here use the repository's plain CheckPermission
// (no audit side effect), not the auditing plugins.CheckPermission — a
// device-plugin declaring only the tcp:* wildcard (the documented common
// case: "configurable terminal plugins learn their device address from
// install-time settings") would otherwise fail the exact-address probe on
// EVERY write/read of an active protocol exchange, writing a spurious
// "permission_denied" audit_log row for a call that then succeeds one line
// later on the wildcard. A genuine denial (neither probe grants) still gets
// audited, via the same two auditing calls hostTCPOpen already made before
// this helper existed — reviewed 2026-08-22 (ut-docs#606 review finding,
// non-blocking).
func tcpAddrAuthorized(ctx context.Context, s *hostState, addr string) bool {
	repo := data.NewPluginRepo(s.db)
	if granted, exists, err := repo.CheckPermission(ctx, s.pluginID, "tcp:"+addr); err == nil && exists && granted {
		return true
	}
	if granted, exists, err := repo.CheckPermission(ctx, s.pluginID, "tcp:*"); err == nil && exists && granted {
		return true
	}
	// Genuine denial: audit it, same as hostTCPOpen's pre-existing pattern.
	_ = CheckPermission(ctx, s.db, s.pluginID, "tcp:"+addr)
	_ = CheckPermission(ctx, s.db, s.pluginID, "tcp:*")
	return false
}

// hostTCPWrite writes the guest buffer to the connection under a fixed 10s
// write deadline. Returns bytes written or a hostErr* code.
func hostTCPWrite(ctx context.Context, m api.Module, handle int32, ptr, length uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	conn, addr, ok := tcpConns.GetWithAddr(s.pluginID, handle)
	if !ok {
		return hostErrNotFound
	}
	if !tcpAddrAuthorized(ctx, s, addr) {
		return hostErrDenied
	}
	buf, ok := readGuest(m, ptr, length)
	if !ok {
		return hostErrInvalid
	}
	// effectiveDeadline (review finding B1): the fixed 10s write deadline
	// must not outlive ctx's own (shorter) event deadline.
	_ = conn.SetWriteDeadline(effectiveDeadline(ctx, tcpWriteTimeout))
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
	conn, addr, ok := tcpConns.GetWithAddr(s.pluginID, handle)
	if !ok {
		return hostErrNotFound
	}
	if !tcpAddrAuthorized(ctx, s, addr) {
		return hostErrDenied
	}
	if dstCap == 0 {
		return hostErrInvalid
	}
	bufCap := dstCap
	if bufCap > tcpReadBufCap {
		bufCap = tcpReadBufCap
	}
	// Validate the destination region is addressable BEFORE touching the
	// socket (ut-docs#614): conn.Read has no seek-back, so a bad dstPtr
	// discovered only after the read would silently and permanently lose
	// whatever the device just sent. Catching it here means an invalid
	// pointer costs the guest nothing — the bytes are still in the socket's
	// receive buffer for a retry with a good one.
	if _, ok := m.Memory().Read(dstPtr, bufCap); !ok {
		return hostErrInvalid
	}
	// effectiveDeadline (review finding B1): the actual cause of the 25s
	// checkout freeze the review measured — a guest-clamped 30s read
	// deadline outliving the plugin's real 10s event deadline. Clamping by
	// ctx here is what makes HandleEvent's own timeout actually bound this
	// call, matching hostHTTPRequest's ctx-bound http.NewRequestWithContext.
	_ = conn.SetReadDeadline(effectiveDeadline(ctx, clampTCPTimeoutMs(timeoutMs, maxTCPReadTimeoutMs)))
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
