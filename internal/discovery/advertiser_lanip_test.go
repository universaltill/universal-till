package discovery

import (
	"context"
	"errors"
	"net"
	"testing"
)

// A till with no LAN address must publish NOTHING. Until ut-docs#1501 it
// advertised 127.0.0.1 instead — a record every till on the LAN could see,
// listing a shop that could never be joined, whose failure surfaced on the
// other device as "cannot reach that primary" (ut-docs#1499, real hardware).
func TestAdvertiser_DoesNotAdvertiseWithoutALANAddress(t *testing.T) {
	prev := lanIPs
	lanIPs = func() []net.IP { return nil }
	t.Cleanup(func() { lanIPs = prev })

	a, starts, _ := newTestAdvertiser(t, true)
	_, err := a.startLocked(context.Background())
	if !errors.Is(err, ErrNoLANAddress) {
		t.Fatalf("startLocked() error = %v, want ErrNoLANAddress", err)
	}
	if *starts != 0 {
		t.Fatalf("built an mDNS zone with no LAN address to put in it (%d starts)", *starts)
	}
}

// tick() must treat "no address yet" as a quiet retry, not a dead advertiser:
// a till whose Wi-Fi associates a few seconds after boot has to start
// advertising on its own, with no operator action.
func TestAdvertiser_RetriesOnceALANAddressAppears(t *testing.T) {
	var ips []net.IP
	prev := lanIPs
	lanIPs = func() []net.IP { return ips }
	t.Cleanup(func() { lanIPs = prev })

	a, starts, _ := newTestAdvertiser(t, true)

	a.tick(context.Background()) // no address yet
	if *starts != 0 || a.server != nil {
		t.Fatalf("advertised with no LAN address (starts=%d)", *starts)
	}

	ips = []net.IP{net.IPv4(192, 168, 1, 163)}
	a.tick(context.Background())
	if *starts != 1 || a.server == nil {
		t.Fatalf("did not start advertising once an address appeared (starts=%d)", *starts)
	}
}

// The SRV target must not be the OS hostname: on Android that is literally
// "localhost", and a third-party mDNS client resolving it lands on its own
// loopback however correct the A record is.
func TestMDNSHostName_IsTheTillIDNotTheOSHostname(t *testing.T) {
	got := mdnsHostName("541823ba-8122-41b1-93b4-f7662e1a9963")
	if want := "541823ba-8122-41b1-93b4-f7662e1a9963.local."; got != want {
		t.Fatalf("mdnsHostName() = %q, want %q", got, want)
	}
}
