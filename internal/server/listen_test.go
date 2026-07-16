package server

import (
	"net"
	"testing"
)

// A free port is bound exactly as requested.
func TestListenWithFallback_FreePort(t *testing.T) {
	ln, addr, err := listenWithFallback("127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind free port: %v", err)
	}
	defer ln.Close()
	if _, _, err := net.SplitHostPort(addr); err != nil {
		t.Fatalf("returned addr %q not host:port: %v", addr, err)
	}
	if addr != ln.Addr().String() {
		t.Fatalf("addr %q != listener addr %q", addr, ln.Addr().String())
	}
}

// When the requested port is taken, we bind a different (free) port instead of
// failing — the whole point of the fallback.
func TestListenWithFallback_BusyPort(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy a port: %v", err)
	}
	defer busy.Close()

	ln, addr, err := listenWithFallback(busy.Addr().String())
	if err != nil {
		t.Fatalf("fallback bind: %v", err)
	}
	defer ln.Close()
	if addr == busy.Addr().String() {
		t.Fatalf("fallback returned the busy addr %q", addr)
	}
}
