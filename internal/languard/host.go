package languard

import (
	"net"
	"net/netip"
	"strings"
)

var sharedOwn = &ownAddrCache{}

// HostAllowed reports whether an HTTP Host header names this machine: a
// loopback name or address, one of its own interface addresses, or an
// address inside an allowed network (UT_UI_ALLOW_CIDRS). Any other name,
// including one that merely resolves here, is refused. A page reached
// through a DNS-rebound name is same-origin with the till but carries the
// attacker's Host (ADR-0166 §2).
func HostAllowed(host string, allow []*net.IPNet) bool {
	return hostAllowed(host, allow, sharedOwn.get)
}

func hostAllowed(host string, allow []*net.IPNet, own func() []net.IP) bool {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false // a name other than localhost
	}
	a = a.Unmap()
	if a.IsLoopback() {
		return true
	}
	ip := net.IP(a.AsSlice())
	for _, n := range allow {
		if n.Contains(ip) {
			return true
		}
	}
	for _, o := range own() {
		if oa, ok := netip.AddrFromSlice(o); ok && oa.Unmap() == a {
			return true
		}
	}
	return false
}
