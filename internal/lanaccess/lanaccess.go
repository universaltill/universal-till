// Package lanaccess decides whether OTHER devices on the shop network may
// reach this till at all (ut-docs#6955, ADR-0086, ADR-0198).
//
// The desktop shell (Mac/Windows) used to bind 127.0.0.1 only, so no phone or
// second till could ever pair with a desktop main till. Binding every
// interface fixes that but would expose the till's whole HTTP surface to
// whatever network the laptop sits on (a café, a hotel). So the shell binds
// all interfaces and wraps the listener in Wrap: a connection from this
// machine is always served; a connection from anywhere else is closed before
// a single byte is read unless Allowed says so. Allowed is closed by default
// (no policy installed = nobody else gets in).
//
// What an allowed peer can reach is exactly what any LAN peer reaches on a Pi
// or Android main till: the normal routes, each behind its own sign-in or
// bearer (pairing endpoints are rate-limited, codes are one-time).
package lanaccess

import (
	"net"
	"sync"
	"time"
)

var (
	mu          sync.Mutex
	policy      func() bool
	windowUntil time.Time
	now         = time.Now
)

// SetPolicy installs the decision (the till's "Allow other tills on this
// network" setting). nil closes the gate.
func SetPolicy(f func() bool) {
	mu.Lock()
	defer mu.Unlock()
	policy = f
}

// OpenWindow keeps the gate open for d (a pairing code was just shown).
func OpenWindow(d time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	if until := now().Add(d); until.After(windowUntil) {
		windowUntil = until
	}
}

// CloseWindow ends any open pairing window (tests, and a manual "stop").
func CloseWindow() {
	mu.Lock()
	defer mu.Unlock()
	windowUntil = time.Time{}
}

// WindowOpen reports whether a pairing window is currently open.
func WindowOpen() bool {
	mu.Lock()
	defer mu.Unlock()
	return now().Before(windowUntil)
}

// Allowed reports whether a non-local peer may connect right now.
func Allowed() bool {
	mu.Lock()
	f := policy
	mu.Unlock()
	return f != nil && f()
}

// IsLocal reports whether a remote address is this machine itself: loopback,
// or one of its own interface addresses (the Mac opening its own LAN URL).
func IsLocal(a net.Addr) bool {
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		host = a.String()
	}
	if i := indexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, ia := range addrs {
		if n, ok := ia.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// Wrap gates ln: non-local peers are dropped unless Allowed.
func Wrap(ln net.Listener) net.Listener { return &gated{Listener: ln} }

type gated struct{ net.Listener }

func (g *gated) Accept() (net.Conn, error) {
	for {
		c, err := g.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if IsLocal(c.RemoteAddr()) || Allowed() {
			return c, nil
		}
		_ = c.Close()
	}
}
