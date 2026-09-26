package plugins

// Plugin egress policy for http_request AND tcp_open (ut-docs#2891).
//
// The permission check in hostHTTPRequest looks at the URL's host NAME. That
// alone is not enough: a name can resolve (or be re-resolved — DNS
// rebinding) to a LAN or loopback address, and a redirect can point anywhere.
// So every connection a plugin makes is also checked at DIAL time, against
// the IP actually being connected to:
//
//   - a public address is allowed whenever the name check passed
//     (net:<host> or net:*);
//   - a non-public address (loopback, RFC1918, link-local, CGNAT, ULA,
//     multicast, reserved, and their IPv4-mapped / NAT64 / 6to4 forms) only
//     when the plugin holds the EXACT permission for the host it asked for —
//     net:<host> (an ERP webhook on the shop network, Ollama on the till) or
//     tcp:<host>:<port> (a LAN payment terminal), or the setting-bound form
//     net:@setting:<urlKey> / tcp:@setting:<hostKey>:<portKey> naming the
//     address an admin configured (permission_setting.go, ut-docs#2899).
//     net:* and tcp:* grant public addresses only;
//   - the till's own listen port on loopback or on one of its own addresses
//     is always refused, whatever the permission — a plugin must never be
//     able to drive the till's own API;
//   - the unspecified address (0.0.0.0, ::) is always refused.
//
// Redirects re-apply the scheme rule, the permission check for the new host
// and (through the same dialer) the IP rule, capped at maxPluginRedirects.
// No proxy is consulted (a proxy would move the dial off the checked IP) and
// connections are never pooled, so a connection dialled for one plugin's
// explicit LAN grant can never be reused by another plugin's request.

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const maxPluginRedirects = 5

// errEgressDenied marks a request the egress policy refused (as opposed to a
// network failure); hostHTTPRequest maps it to hostErrDenied.
var errEgressDenied = errors.New("plugin egress denied")

// egressDeniedError carries what to log — host and reason, never the URL
// (its query string can hold tokens).
type egressDeniedError struct {
	host   string
	ip     string
	reason string
}

func (e *egressDeniedError) Error() string {
	if e.ip != "" {
		return fmt.Sprintf("plugin egress denied: host %s (%s): %s", e.host, e.ip, e.reason)
	}
	return fmt.Sprintf("plugin egress denied: host %s: %s", e.host, e.reason)
}

func (e *egressDeniedError) Unwrap() error { return errEgressDenied }

// tillListenPort is the port this till's HTTP server listens on (0 until
// known). Set from config at plugin init and again from the address the
// server actually bound (it may fall back to another port).
var tillListenPort atomic.Int32

// SetTillListenAddr records the till's own listen address so plugin egress
// can refuse it. An unparseable address leaves the previous value.
func SetTillListenAddr(addr string) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return
	}
	tillListenPort.Store(int32(port))
}

// nonPublicPrefixes are never reachable under net:* (see the file comment).
var nonPublicPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		// IPv4
		"0.0.0.0/8",       // "this network", includes unspecified
		"10.0.0.0/8",      // RFC1918
		"100.64.0.0/10",   // CGNAT
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local, cloud metadata
		"172.16.0.0/12",   // RFC1918
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"192.88.99.0/24",  // 6to4 relay anycast
		"192.168.0.0/16",  // RFC1918
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved, includes broadcast
		// IPv6
		"::/128",        // unspecified
		"::1/128",       // loopback
		"::/96",         // deprecated IPv4-compatible
		"100::/64",      // discard-only
		"2001::/23",     // IETF protocol assignments, includes Teredo
		"2001:db8::/32", // documentation
		"fc00::/7",      // unique local
		"fe80::/10",     // link-local
		"fec0::/10",     // deprecated site-local
		"ff00::/8",      // multicast
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var (
	nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour   = netip.MustParsePrefix("2002::/16")
)

// isPublicIP reports whether ip is a globally routable unicast address.
// IPv4-mapped, NAT64 and 6to4 forms are judged by the IPv4 address they
// embed, so ::ffff:10.0.0.1 or 2002:a00:1:: cannot smuggle a LAN target.
func isPublicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if a.Is6() {
		b := a.As16()
		switch {
		case nat64Prefix.Contains(a):
			return isPublicIP(net.IP(b[12:16]))
		case sixToFour.Contains(a):
			return isPublicIP(net.IP(b[2:6]))
		}
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// egressGrants is the set of hosts (lower-cased) this request chain holds an
// EXACT net:<host> permission for — the only hosts whose non-public
// addresses may be dialled. hostHTTPRequest seeds it; CheckRedirect adds each
// redirect target it approves. One chain runs sequentially, but the mutex
// keeps it safe should the transport ever dial concurrently.
type egressGrants struct {
	mu    sync.Mutex
	exact map[string]bool
}

type egressGrantsKey struct{}

func (g *egressGrants) add(host string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.exact[normGrantHost(host)] = true
}

func (g *egressGrants) has(host string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.exact[normGrantHost(host)]
}

func withEgressGrants(ctx context.Context, g *egressGrants) context.Context {
	return context.WithValue(ctx, egressGrantsKey{}, g)
}

func egressGrantsFrom(ctx context.Context) *egressGrants {
	g, _ := ctx.Value(egressGrantsKey{}).(*egressGrants)
	return g
}

// egressDialer is the dial-time half of the policy. Every function is
// injectable so tests can simulate public names and LAN IPs.
type egressDialer struct {
	lookup   func(ctx context.Context, host string) ([]net.IP, error)
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	localIPs func() []net.IP
	tillPort func() int
}

func defaultEgressDialer() *egressDialer {
	nd := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &egressDialer{
		lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
		dial: nd.DialContext,
		localIPs: func() []net.IP {
			addrs, err := net.InterfaceAddrs()
			if err != nil {
				return nil
			}
			out := make([]net.IP, 0, len(addrs))
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok {
					out = append(out, n.IP)
				}
			}
			return out
		},
		tillPort: func() int { return int(tillListenPort.Load()) },
	}
}

// DialContext is the http.Transport hook: the exact grant for the host comes
// from the request chain's egressGrants.
func (d *egressDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	exact := false
	if host, _, err := net.SplitHostPort(addr); err == nil {
		if g := egressGrantsFrom(ctx); g != nil {
			exact = g.has(host)
		}
	}
	conn, _, err := d.dialChecked(ctx, network, addr, exact)
	return conn, err
}

// dialChecked resolves addr itself, checks every candidate IP, and connects
// only to an allowed one — by IP, so what is dialled is what was checked.
// exact is whether the caller holds the exact permission for this host
// (net:<host> for http_request, tcp:<host>:<port> for tcp_open) — the only
// thing that unlocks a non-public address. It also returns the IP it
// checked and connected to.
func (d *egressDialer) dialChecked(ctx context.Context, network, addr string, exact bool) (net.Conn, net.IP, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid port in %q", addr)
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = d.lookup(ctx, host)
		if err != nil {
			return nil, nil, err
		}
		if len(ips) == 0 {
			return nil, nil, fmt.Errorf("no addresses for host %s", host)
		}
	}
	var firstErr error
	for _, ip := range ips {
		if err := d.check(host, ip, port, exact); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		conn, err := d.dial(ctx, network, net.JoinHostPort(ip.String(), portStr))
		if err == nil {
			return conn, ip, nil
		}
		firstErr = err
	}
	return nil, nil, firstErr
}

func (d *egressDialer) check(host string, ip net.IP, port int, exact bool) error {
	deny := func(reason string) error {
		return &egressDeniedError{host: host, ip: ip.String(), reason: reason}
	}
	if ip.IsUnspecified() {
		return deny("unspecified address")
	}
	if tp := d.tillPort(); tp != 0 && port == tp && d.isTillAddress(ip) {
		return deny("the till's own listen port")
	}
	if !isPublicIP(ip) && !exact {
		return deny("non-public address needs the exact permission for " + host)
	}
	return nil
}

func (d *egressDialer) isTillAddress(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	for _, own := range d.localIPs() {
		if own.Equal(ip) {
			return true
		}
	}
	return false
}

// newPluginHTTPClient builds the http_request client around d. tlsCfg is nil
// in production (system roots); tests pass their own root pool.
func newPluginHTTPClient(d *egressDialer, tlsCfg *tls.Config) *http.Client {
	tr := &http.Transport{
		Proxy:                  nil, // a proxy would move the dial off the checked IP
		DialContext:            d.DialContext,
		TLSClientConfig:        tlsCfg,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		ExpectContinueTimeout:  1 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		DisableKeepAlives:      true, // never share a connection across grants
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       2 * time.Minute, // backstop; the event deadline is usually tighter
		CheckRedirect: checkPluginRedirect,
	}
}

// defaultPluginHTTPClient serves every plugin's http_request in production.
var defaultPluginHTTPClient = newPluginHTTPClient(defaultEgressDialer(), nil)

// checkPluginRedirect re-applies the scheme rule and the permission check to
// each redirect target; the IP rule follows through the dialer, using the
// grant recorded here.
func checkPluginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxPluginRedirects {
		return fmt.Errorf("stopped after %d redirects", maxPluginRedirects)
	}
	host := req.URL.Hostname()
	if !hostAllowedScheme(req.URL) {
		return &egressDeniedError{host: host, reason: "redirect to scheme " + req.URL.Scheme}
	}
	ctx := req.Context()
	s, ok := stateFrom(ctx)
	if !ok {
		return &egressDeniedError{host: host, reason: "no plugin context"}
	}
	exact, err := netPermission(ctx, s.db, s.pluginID, host)
	if err != nil {
		return err
	}
	if exact {
		if g := egressGrantsFrom(ctx); g != nil {
			g.add(host)
		}
	}
	return nil
}

// netPermission checks the name half of the policy: the plugin must hold
// net:<host>, a net:@setting:<urlKey> whose stored URL names this host
// (ut-docs#2899), or net:*. exact reports either of the first two — only an
// exact grant unlocks non-public addresses at dial time. Hosts are compared
// normalised (normGrantHost). Only a genuine denial is audited.
func netPermission(ctx context.Context, db *sql.DB, pluginID, host string) (exact bool, err error) {
	exact, wildcard, err := netGrantMatch(ctx, db, pluginID, host)
	if err != nil {
		return false, &egressDeniedError{host: host, reason: "permission lookup failed"}
	}
	if exact {
		return true, nil
	}
	if wildcard {
		return false, nil
	}
	_ = CheckPermission(ctx, db, pluginID, "net:"+host) // audit the denial
	return false, &egressDeniedError{host: host, reason: "no net:" + host + ", matching net:@setting or net:* permission"}
}
