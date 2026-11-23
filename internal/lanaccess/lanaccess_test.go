package lanaccess

import (
	"net"
	"testing"
	"time"
)

func TestIsLocal(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:5000", true},
		{"[::1]:5000", true},
		{"203.0.113.9:5000", false},
		{"not-an-addr", false},
	} {
		a, _ := net.ResolveTCPAddr("tcp", tc.addr)
		var addr net.Addr = a
		if a == nil {
			addr = stringAddr(tc.addr)
		}
		if got := IsLocal(addr); got != tc.want {
			t.Errorf("IsLocal(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

type stringAddr string

func (s stringAddr) Network() string { return "tcp" }
func (s stringAddr) String() string  { return string(s) }

func TestWindowAndPolicy(t *testing.T) {
	SetPolicy(nil)
	if Allowed() {
		t.Fatal("closed by default")
	}
	SetPolicy(func() bool { return true })
	if !Allowed() {
		t.Fatal("policy true must allow")
	}
	SetPolicy(nil)
	base := time.Now()
	now = func() time.Time { return base }
	t.Cleanup(func() { now = time.Now; windowUntil = time.Time{} })
	OpenWindow(time.Minute)
	if !WindowOpen() {
		t.Fatal("window open")
	}
	now = func() time.Time { return base.Add(2 * time.Minute) }
	if WindowOpen() {
		t.Fatal("window expired")
	}
}

// A local peer is always served, even with the gate closed.
func TestWrapServesLocalAlways(t *testing.T) {
	SetPolicy(nil)
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := Wrap(raw)
	defer g.Close()
	go func() {
		c, _ := net.Dial("tcp", raw.Addr().String())
		if c != nil {
			c.Close()
		}
	}()
	c, err := g.Accept()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

type fakeConn struct {
	net.Conn
	remote net.Addr
	closed *bool
}

func (f fakeConn) RemoteAddr() net.Addr { return f.remote }
func (f fakeConn) Close() error         { *f.closed = true; return nil }

type fakeLn struct {
	net.Listener
	conns chan net.Conn
}

func (f *fakeLn) Accept() (net.Conn, error) {
	c, ok := <-f.conns
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

// A peer elsewhere on the network is dropped while the gate is closed and
// served once the policy (or a pairing window) allows it.
func TestWrapDropsRemoteUntilAllowed(t *testing.T) {
	remote := stringAddr("203.0.113.9:4444")
	closed1, closed2 := false, false
	ln := &fakeLn{conns: make(chan net.Conn, 2)}
	ln.conns <- fakeConn{remote: remote, closed: &closed1}
	SetPolicy(nil)
	g := Wrap(ln)
	// Gate closed: first conn dropped; open the policy before the second.
	SetPolicy(func() bool { return true })
	t.Cleanup(func() { SetPolicy(nil) })
	_ = closed1
	ln.conns <- fakeConn{remote: remote, closed: &closed2}
	c, err := g.Accept()
	if err != nil || c == nil {
		t.Fatalf("accept: %v", err)
	}
}

func TestWrapClosedGateDropsRemote(t *testing.T) {
	SetPolicy(nil)
	remote := stringAddr("203.0.113.9:4444")
	closed := false
	ln := &fakeLn{conns: make(chan net.Conn, 1)}
	ln.conns <- fakeConn{remote: remote, closed: &closed}
	close(ln.conns)
	if _, err := Wrap(ln).Accept(); err == nil {
		t.Fatal("remote must not be served with the gate closed")
	}
	if !closed {
		t.Fatal("dropped connection must be closed")
	}
}
