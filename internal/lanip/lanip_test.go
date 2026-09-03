package lanip

import (
	"errors"
	"net"
	"testing"
	"time"
)

// fakeConn is a net.Conn that only answers LocalAddr and Close — the only two
// methods probe() touches. Everything else panics loudly rather than
// pretending to be a working socket.
type fakeConn struct {
	net.Conn
	local net.Addr
}

func (c *fakeConn) LocalAddr() net.Addr { return c.local }
func (c *fakeConn) Close() error        { return nil }

func stubEnumeration(t *testing.T, ifaces []net.Interface, err error) {
	t.Helper()
	prev := netInterfaces
	netInterfaces = func() ([]net.Interface, error) { return ifaces, err }
	t.Cleanup(func() { netInterfaces = prev })
}

func stubProbe(t *testing.T, dial func(network, addr string) (net.Conn, error)) {
	t.Helper()
	prev := probeDial
	probeDial = dial
	t.Cleanup(func() { probeDial = prev })
}

// An Android till: enumeration is denied (Android 11+ withholds the netlink
// access net.Interfaces needs), so it comes back empty on a device that does
// have a Wi-Fi address. This is the exact state that made every pairing
// attempt in ut-docs#1499 fail, and the whole reason this package exists.
func TestIPv4s_FallsBackToRouteProbeWhenEnumerationIsDenied(t *testing.T) {
	stubEnumeration(t, nil, errors.New("route ip+net: netlinkrib: permission denied"))
	var dialled []string
	stubProbe(t, func(network, addr string) (net.Conn, error) {
		dialled = append(dialled, network+" "+addr)
		return &fakeConn{local: &net.UDPAddr{IP: net.IPv4(192, 168, 1, 136), Port: 51234}}, nil
	})

	got := IPv4s()
	if len(got) != 1 || got[0].String() != "192.168.1.136" {
		t.Fatalf("IPv4s() = %v, want the probed LAN address 192.168.1.136", got)
	}
	if len(dialled) == 0 || dialled[0] != "udp4 224.0.0.251:5353" {
		t.Fatalf("first probe was %v, want the link-local mDNS group — a shop LAN "+
			"with no gateway must still resolve an address", dialled)
	}
}

// The failure that shipped: rather than advertise an address no peer can use,
// report none. A loopback advertisement produces a candidate that looks
// joinable and then fails on the OTHER device, which is what made #1499 so
// hard to read.
func TestIPv4s_NeverReturnsLoopback(t *testing.T) {
	stubEnumeration(t, nil, nil) // no interfaces at all
	stubProbe(t, func(network, addr string) (net.Conn, error) {
		return &fakeConn{local: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}}, nil
	})

	if got := IPv4s(); got != nil {
		t.Fatalf("IPv4s() = %v, want nil rather than a loopback address", got)
	}
}

func TestIPv4_ReportsAnOperatorFacingErrorWhenThereIsNoAddress(t *testing.T) {
	stubEnumeration(t, nil, nil)
	stubProbe(t, func(network, addr string) (net.Conn, error) {
		return nil, errors.New("network is unreachable")
	})

	got, err := IPv4()
	if err == nil {
		t.Fatalf("IPv4() = %q, want an error when the till has no LAN address", got)
	}
}

// Enumeration stays the primary path: it is the only one that can report
// MULTIPLE addresses (a till on Wi-Fi and Ethernet at once), and the probe
// must not run when it succeeds — a probe is a route lookup, not free.
func TestIPv4s_PrefersEnumerationAndSkipsTheProbe(t *testing.T) {
	realIfaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("cannot enumerate interfaces on this host: %v", err)
	}
	if len(enumerate()) == 0 {
		t.Skip("this host has no non-loopback IPv4 — nothing to prefer")
	}
	stubEnumeration(t, realIfaces, nil)
	stubProbe(t, func(network, addr string) (net.Conn, error) {
		t.Fatalf("probe dialled %s %s even though enumeration succeeded", network, addr)
		return nil, nil
	})

	if got := IPv4s(); len(got) == 0 {
		t.Fatal("IPv4s() returned nothing on a host that has a LAN address")
	}
}

// A probe that hangs must not hang the till's pairing screen behind it. This
// pins that probe() closes what it opens, so a slow target cannot leak a
// socket per call.
func TestProbe_ClosesTheSocketItOpens(t *testing.T) {
	closed := make(chan struct{}, 1)
	stubEnumeration(t, nil, nil)
	stubProbe(t, func(network, addr string) (net.Conn, error) {
		return &closeRecordingConn{
			fakeConn: fakeConn{local: &net.UDPAddr{IP: net.IPv4(10, 0, 0, 5)}},
			closed:   closed,
		}, nil
	})

	if got := IPv4s(); len(got) != 1 {
		t.Fatalf("IPv4s() = %v, want the probed address", got)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("probe() left its socket open")
	}
}

type closeRecordingConn struct {
	fakeConn
	closed chan struct{}
}

func (c *closeRecordingConn) Close() error {
	select {
	case c.closed <- struct{}{}:
	default:
	}
	return nil
}
