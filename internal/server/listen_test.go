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
// failing — the whole point of the fallback. A non-wildcard configured host
// (127.0.0.1, as used here) must also keep that same host on fallback — only
// a wildcard host degrades (TestListenWithFallback_WildcardHostFallsBackToLoopback).
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
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split fallback addr %q: %v", addr, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("fallback addr %q lost the originally configured loopback host", addr)
	}
}

// ut-docs#1169: the configured port (":8080" by default — an intentional,
// LAN-reachable wildcard bind for self-order/pairing) is reachable off-device
// by design, but a FALLBACK means something else already holds that port —
// in practice, a second, unconfigured instance racing this one at boot. That
// fallback must never repeat the wildcard bind: it degrades to loopback-only
// instead of silently exposing an unconfigured server to the whole LAN.
// Table-driven over every spelling net.Listen itself treats as "every
// interface" (an independent review proved, by direct probe, that a
// string-literal check covering only ""/"0.0.0.0"/"::" still leaks a
// wildcard bind for "::0", "0:0:0:0:0:0:0:0" and "::ffff:0.0.0.0" — all of
// which net.Listen also binds to every interface).
func TestListenWithFallback_WildcardHostFallsBackToLoopback(t *testing.T) {
	wildcardHosts := []string{"", "0.0.0.0", "::", "::0", "0:0:0:0:0:0:0:0", "::ffff:0.0.0.0"}
	for _, host := range wildcardHosts {
		t.Run(host, func(t *testing.T) {
			// ut-docs#1413: occupy the port on the SAME host spelling under
			// test, not on a hardcoded "127.0.0.1:0". A port already held on
			// 127.0.0.1 collides with a wildcard bind on the same port on
			// Linux, but on macOS a dual-stack wildcard bind can succeed
			// alongside an existing 127.0.0.1 listener on that port — so the
			// collision this test relies on to drive listenWithFallback into
			// its fallback path silently stopped happening there, and the
			// fallback-degrades-to-loopback behaviour below went untested on
			// darwin even though CI (Linux) stayed green. Binding the exact
			// same address (host *and* port) twice fails identically on
			// every OS, so this can't drift out of sync with the platform.
			busy, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
			if err != nil {
				t.Fatalf("occupy a port on %q: %v", host, err)
			}
			defer busy.Close()
			_, portStr, err := net.SplitHostPort(busy.Addr().String())
			if err != nil {
				t.Fatalf("split busy addr: %v", err)
			}

			// A second bind on the identical wildcard host+port fails on
			// every OS — that failure is what drives listenWithFallback
			// into its fallback path.
			addr := net.JoinHostPort(host, portStr)
			ln, actual, err := listenWithFallback(addr)
			if err != nil {
				t.Fatalf("fallback bind for %q: %v", addr, err)
			}
			defer ln.Close()

			actualHost, _, err := net.SplitHostPort(actual)
			if err != nil {
				t.Fatalf("split fallback addr %q: %v", actual, err)
			}
			if actualHost != "127.0.0.1" {
				t.Fatalf("fallback from wildcard host %q bound %q — want loopback-only (127.0.0.1), never wildcard", host, actual)
			}
		})
	}
}
