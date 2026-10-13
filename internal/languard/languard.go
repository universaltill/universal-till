// Package languard keeps the till UI on the till's own machine (ADR-0157,
// ut-docs#4261).
//
// The till listens on every interface because paired tills sync over the
// LAN. Before this guard, everything else on that port answered the LAN too:
// a phone on the shop Wi-Fi could open the PIN screen and brute-force a
// 4-digit PIN against the device-wide lockout. The guard is the outermost
// handler, so a LAN caller gets a 403 before auth ever runs.
//
// A caller is local when its TCP peer (RemoteAddr — never a forwarded-for
// header) is a loopback address, one of this machine's own interface
// addresses, or inside an explicitly allowed network (UT_UI_ALLOW_CIDRS).
// Every other caller reaches only lanPath's surface.
package languard

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"path"
	"strings"
	"sync"
	"time"
)

// minPrefixBits is the widest network UT_UI_ALLOW_CIDRS accepts. Anything
// wider (0.0.0.0/0, ::/0, …) is the guard switched off, not a trusted network.
const minPrefixBits = 8

// ownAddrsTTL bounds how stale the cached interface addresses can be; DHCP
// can move the till's LAN address while it runs.
const ownAddrsTTL = 30 * time.Second

// ParseCIDRs parses UT_UI_ALLOW_CIDRS entries: CIDRs or bare IPs (a
// single-host network). Blank entries are skipped. Any invalid or
// allow-everything entry is an error, so a typo never widens the guard.
func ParseCIDRs(entries []string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !strings.Contains(e, "/") {
			ip := net.ParseIP(e)
			if ip == nil {
				return nil, fmt.Errorf("UT_UI_ALLOW_CIDRS: %q is not an IP or CIDR", e)
			}
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(e)
		if err != nil {
			return nil, fmt.Errorf("UT_UI_ALLOW_CIDRS: %q is not an IP or CIDR", e)
		}
		if ones, _ := n.Mask.Size(); ones < minPrefixBits {
			return nil, fmt.Errorf("UT_UI_ALLOW_CIDRS: %q is wider than /%d", e, minPrefixBits)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

// New wraps next so only local callers (see the package doc) reach all of
// it; LAN callers reach lanPath's surface and get 403 for everything else.
func New(next http.Handler, allow []*net.IPNet) http.Handler {
	c := &ownAddrCache{}
	return newWithOwn(next, allow, c.get)
}

func newWithOwn(next http.Handler, allow []*net.IPNet, own func() []net.IP) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lanPath(r.URL.Path) || local(r.RemoteAddr, allow, own) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
	})
}

// lanPath is the surface a LAN caller may reach (ADR-0157 §2):
//   - /api/sync/*: till-to-till sync; each handler authenticates its caller
//     (per-till bearer) or is unauthenticated by design and rate-limited.
//   - /healthz.
//   - the anonymous guest pages and the static files they load, until they
//     move to the cloud (ADR-0157 §4).
//
// A path that is not already clean ("/api/sync/../auth/login",
// "/healthz/") is refused outright: the mux would only redirect it, and
// judging the cleaned string while auth and the mux see the raw one would let
// the three layers disagree.
func lanPath(p string) bool {
	if path.Clean("/"+p) != p {
		return false
	}
	if p == "/healthz" || p == "/self-order" {
		return true
	}
	for _, prefix := range []string{
		"/api/sync/",
		"/self-order/", "/api/self-order/", "/o/",
		"/public/", "/themes/", "/plugin-icons/",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// local reports whether the TCP peer is on this machine or an allowed
// network. An unparseable peer is not local. The zone of a link-local peer
// ("fe80::1%eth0") is dropped and an IPv4-mapped address unmapped before
// comparing, so the till opened at its own link-local address still counts.
func local(remoteAddr string, allow []*net.IPNet, own func() []net.IP) bool {
	ap, err := netip.ParseAddrPort(remoteAddr)
	if err != nil {
		return false
	}
	a := ap.Addr().WithZone("").Unmap()
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

// ownAddrCache lists this machine's interface addresses, refreshed at most
// every ownAddrsTTL. A lookup error keeps the previous list (an empty list
// on the first call): it can only make the guard stricter.
type ownAddrCache struct {
	mu   sync.Mutex
	at   time.Time
	ips  []net.IP
	addr func() ([]net.Addr, error)
}

func (c *ownAddrCache) get() []net.IP {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) < ownAddrsTTL {
		return c.ips
	}
	lookup := c.addr
	if lookup == nil {
		lookup = net.InterfaceAddrs
	}
	c.at = time.Now()
	addrs, err := lookup()
	if err != nil {
		return c.ips
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			ips = append(ips, n.IP)
		}
	}
	c.ips = ips
	return ips
}
