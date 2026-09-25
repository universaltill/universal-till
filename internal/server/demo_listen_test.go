package server

import (
	"net"
	"testing"
)

// ADR-0113 §1.8 (ut-docs#2687): a demo till binds exactly the address the
// broker gave it and fails if it can't — never listenWithFallback's nearby
// port, or the broker would proxy a visitor to the wrong process.
func TestBindListener_DemoBusyPortFailsInsteadOfFallingBack(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy a port: %v", err)
	}
	defer busy.Close()

	ln, addr, err := bindListener(busy.Addr().String(), true)
	if err == nil {
		ln.Close()
		t.Fatalf("demo bind on a busy port succeeded on %q; want an error", addr)
	}

	// Demo off keeps today's fallback exactly.
	ln, addr, err = bindListener(busy.Addr().String(), false)
	if err != nil {
		t.Fatalf("non-demo fallback bind: %v", err)
	}
	defer ln.Close()
	if addr == busy.Addr().String() {
		t.Fatalf("non-demo fallback returned the busy addr %q", addr)
	}
}

func TestBindListener_DemoFreePortBindsExactly(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	want := probe.Addr().String()
	probe.Close()

	ln, addr, err := bindListener(want, true)
	if err != nil {
		t.Fatalf("demo bind on a free port: %v", err)
	}
	defer ln.Close()
	if addr != want {
		t.Fatalf("demo bound %q, want exactly %q", addr, want)
	}
}

// A demo till never opens a browser on the host, whatever UT_OPEN_BROWSER says.
func TestOpenBrowserFor_NeverInDemo(t *testing.T) {
	t.Setenv("UT_OPEN_BROWSER", "1")
	t.Setenv("UT_KIOSK", "")
	if openBrowserFor(true) {
		t.Fatal("demo till would open a browser")
	}
	if !openBrowserFor(false) {
		t.Fatal("non-demo with UT_OPEN_BROWSER=1 must still open a browser")
	}
}
