package plugins

import "net"

// Test-only helpers (ut-docs#2891): production opens handles through
// tcpConnRegistry.open with the egress dialer's verdict and reads them
// through get; the tests keep these older, narrower entry points so the
// whole-program deadcode gate doesn't see unreachable exported code.

// Open registers conn under the plugin's next handle, recording addr (the
// `tcp:<host>:<port>` permission string checked to authorize this dial) for
// later per-call re-checks. Returns (handle, true) or (0, false) when the
// plugin is already at maxTCPHandlesPerPlugin — the caller still owns (and
// must close) conn in that case.
func (r *tcpConnRegistry) Open(pluginID string, conn net.Conn, addr string) (int32, bool) {
	return r.open(pluginID, tcpHandleInfo{conn: conn, addr: addr, nonPublic: connIsNonPublic(conn)})
}

// connIsNonPublic reports whether conn's remote end is a non-public address
// (an unknown address counts as non-public — fail closed).
func connIsNonPublic(conn net.Conn) bool {
	ta, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return true
	}
	return !isPublicIP(ta.IP)
}

// GetWithAddr looks up an open connection by (pluginID, handle), returning
// it together with the `tcp:<host>:<port>` permission string that was
// checked when the handle was opened, in a single locked lookup —
// hostTCPWrite/hostTCPRead need both the connection and its authorization
// address per call, and looking them up as two separately-locked calls
// leaves a re-lock window where a concurrent Close/CloseAll for the same
// handle could interleave between them (reviewed 2026-08-22, ut-docs#606
// review finding, non-blocking: analysis showed the window already fails
// closed or, in the narrowest cross-reload race, degrades to a spurious
// closed-connection I/O error rather than misdirecting a call onto a
// different plugin's connection — this closes the window regardless,
// since a single lock is free to have). It replaced a conn-only Get, whose
// last (test-only) callers moved here under ut-docs#1566 so the tests
// assert through the same lookup production uses.
func (r *tcpConnRegistry) GetWithAddr(pluginID string, handle int32) (net.Conn, string, bool) {
	info, ok := r.get(pluginID, handle)
	return info.conn, info.addr, ok
}
