// Package lanip answers the one question the till asks from several places:
// "what address can another device on this shop's LAN dial me on?"
//
// It exists because interface enumeration — the obvious answer, and the only
// one this codebase used to have — silently returns NOTHING on Android.
// Android 11+ denies an app the netlink/proc access Go's net.Interfaces()
// needs, so net.InterfaceAddrs() comes back empty on a device that plainly
// does have a Wi-Fi address. Two independent call sites hit that and each
// degraded in its own confusing way (ut-docs#1499, proved on real hardware):
//
//   - discovery.Advertiser fell back to advertising 127.0.0.1 over mDNS, so
//     every peer that found the till dialled its OWN loopback and reported
//     "cannot reach that primary";
//   - pages.advertisableHost failed outright, so "show pairing code" answered
//     409 and htmx — which does not swap non-2xx — rendered nothing at all.
//
// The fallback here is a UDP "dial" that sends no packet: connecting a UDP
// socket only asks the kernel to pick a route and bind a source address,
// which is exactly the address a peer would see, and it needs none of the
// enumeration permissions Android withholds.
package lanip

import (
	"fmt"
	"net"
)

// netInterfaces and probeDial are seams so tests can drive the
// enumeration-denied path (an Android device) on a normal machine, which is
// what made this bug untestable before — see sync_api_test.go's note about
// the coverage gap this closes.
var (
	netInterfaces = net.Interfaces
	probeDial     = net.Dial
)

// probeTargets are dialled in order when enumeration yields nothing. The mDNS
// multicast group comes first deliberately: it is link-local, so it needs no
// gateway, no DNS and no internet — the isolated shop LAN this product must
// pair two tills on (ADR-0003, offline-first) — and it is the very address
// discovery already multicasts to, so a host that can advertise at all can
// route to it. The public address is only a second chance for a host whose
// multicast route is missing; nothing is ever sent to either.
var probeTargets = []string{"224.0.0.251:5353", "8.8.8.8:80"}

// usable reports whether ip is an address another device on the LAN could
// actually dial: IPv4, not loopback, not a link-local autoconfiguration
// address, not the unspecified address.
func usable(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	return ip.To4() != nil
}

// IPv4s returns every LAN IPv4 this host can be reached on, enumeration
// first and the route probe as a fallback. It returns nil — never a loopback
// placeholder — when no such address exists: a caller that advertises
// 127.0.0.1 to the LAN is strictly worse off than one that advertises
// nothing, because it produces a candidate that looks joinable and fails on
// the other device.
func IPv4s() []net.IP {
	if ips := enumerate(); len(ips) > 0 {
		return ips
	}
	if ip := probe(); ip != nil {
		return []net.IP{ip}
	}
	return nil
}

// IPv4 returns a single LAN IPv4 for callers that can only carry one (a
// pairing code's URL, for instance). The error text is operator-facing.
func IPv4() (string, error) {
	ips := IPv4s()
	if len(ips) == 0 {
		return "", fmt.Errorf("this till has no network address other than loopback — " +
			"connect it to the shop network before pairing another till")
	}
	return ips[0].To4().String(), nil
}

// enumerate walks up, non-loopback interfaces. Unchanged in spirit from the
// two hand-rolled copies this package replaces.
func enumerate() []net.IP {
	ifaces, err := netInterfaces()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if usable(ip) {
				out = append(out, ip.To4())
			}
		}
	}
	return out
}

// probe asks the kernel which source address it would use to reach a target,
// without sending anything. Every failure is expected on some host somewhere
// (no multicast route, no default route, no network at all), so each target
// is tried in turn and a total failure is reported as "no address", not as an
// error worth showing anyone.
func probe() net.IP {
	for _, target := range probeTargets {
		conn, err := probeDial("udp4", target)
		if err != nil {
			continue
		}
		addr, ok := conn.LocalAddr().(*net.UDPAddr)
		_ = conn.Close()
		if !ok || !usable(addr.IP) {
			continue
		}
		return addr.IP.To4()
	}
	return nil
}
