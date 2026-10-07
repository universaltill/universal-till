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
//   - http_request / http_open additionally need http:lan for a non-public
//     address (ADR-0121 §3, owner decision ut-docs#3794): an exact net:
//     grant alone reaches public addresses and loopback only, and loopback
//     only when the host asked for is itself a loopback name (localhost,
//     127.0.0.0/8, ::1 — Ollama on the till). A refusal for the missing
//     http:lan is audited, naming it. tcp_open keeps its exact tcp: rule;
//   - the till's own listen port on loopback or on one of its own addresses
//     is always refused, whatever the permission — a plugin must never be
//     able to drive the till's own API;
//   - the unspecified address (0.0.0.0, ::) is always refused;
//   - a cloud-metadata address (169.254.169.254, fd00:ec2::254, in any
//     IPv4-mapped / NAT64 / 6to4 spelling) is always refused, whatever the
//     permission — exact grants and http:lan included (ADR-0121 §3);
//   - a host admitted as plain http only because the plugin holds http:lan
//     (ADR-0121 §2, ut-docs#3156) is LAN-only: it must dial a non-public
//     address, so http:lan can never send plain http across the internet.
//
// Redirects re-apply the scheme rule, the permission check for the new host
// and (through the same dialer) the IP rule, capped at maxPluginRedirects.
// No proxy is consulted (a proxy would move the dial off the checked IP) and
// connections are never pooled, so a connection dialled for one plugin's
// explicit LAN grant can never be reused by another plugin's request.
//
// net:validation:<host> (ut-docs#3226, permission_setting.go) passes the name
// check and allows plain http for that one host, but is never exact here —
// not even alongside net:<host> for the same host: it must resolve to a
// public address.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
)

const maxPluginRedirects = 5

// permHTTPLAN (ADR-0121 §2, ut-docs#3156): plain http to an exactly granted
// or endpoint-setting host on a non-public address (admitHTTPHop).
const permHTTPLAN = "http:lan"

// errEgressDenied marks a request the egress policy refused (as opposed to a
// network failure); hostHTTPRequest maps it to hostErrDenied.
var errEgressDenied = errors.New("plugin egress denied")

// egressDeniedError carries what to log — host and reason, never the URL
// (its query string can hold tokens).
type egressDeniedError struct {
	host   string
	ip     string
	reason string
	// missingPerm names the permission whose absence caused the refusal,
	// when one would have allowed it — the dialer audits it (ut-docs#3794).
	missingPerm string
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
//
// validation records the hosts in the chain approved through a
// net:validation:<host> grant (ut-docs#3226) — they pick the response cap,
// and play no part in the dial-time IP rule.
//
// lanOnly records the hosts admitted only through http:lan (ut-docs#3156):
// the dialer refuses a public address for them. Set per hop (setLANOnly), so
// a later hop to the same host decides for itself.
type egressGrants struct {
	mu         sync.Mutex
	exact      map[string]bool
	validation map[string]bool
	lanOnly    map[string]bool
	httpLAN    map[string]bool
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

func (g *egressGrants) addValidation(host string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.validation == nil {
		g.validation = map[string]bool{}
	}
	g.validation[normGrantHost(host)] = true
}

func (g *egressGrants) isValidation(host string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.validation[normGrantHost(host)]
}

func (g *egressGrants) setLANOnly(host string, on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lanOnly == nil {
		g.lanOnly = map[string]bool{}
	}
	g.lanOnly[normGrantHost(host)] = on
}

func (g *egressGrants) isLANOnly(host string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lanOnly[normGrantHost(host)]
}

func (g *egressGrants) setHTTPLAN(host string, on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.httpLAN == nil {
		g.httpLAN = map[string]bool{}
	}
	g.httpLAN[normGrantHost(host)] = on
}

// hasHTTPLAN reports whether the hop to host was admitted by a plugin
// holding http:lan (ut-docs#3794) — what lets an exact grant reach a
// non-loopback LAN address.
func (g *egressGrants) hasHTTPLAN(host string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.httpLAN[normGrantHost(host)]
}

// record stores one approved hop's decision.
func (g *egressGrants) record(host string, d httpHopDecision) {
	if d.exact {
		g.add(host)
	}
	if d.validation {
		g.addValidation(host)
	}
	g.setLANOnly(host, d.lanOnly)
	g.setHTTPLAN(host, d.httpLAN)
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
	// netaccess.DialContext: refuses every dial on the demo till (ADR-0113).
	nd := netaccess.DialContext(10*time.Second, 30*time.Second)
	return &egressDialer{
		lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
		dial: nd,
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
// from the request chain's egressGrants. Every dial here is http_request or
// http_open, so a non-loopback LAN address also needs http:lan
// (needsHTTPLAN, ut-docs#3794); a refusal for it is audited.
func (d *egressDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	pol := egressPolicy{needsHTTPLAN: true}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		if g := egressGrantsFrom(ctx); g != nil {
			pol = egressPolicy{exact: g.has(host), lanOnly: g.isLANOnly(host), needsHTTPLAN: !g.hasHTTPLAN(host)}
		}
	}
	conn, _, err := d.dialChecked(ctx, network, addr, pol)
	var de *egressDeniedError
	if errors.As(err, &de) && de.missingPerm != "" {
		if s, ok := stateFrom(ctx); ok {
			// Detached from the dial's deadline so a refusal near the event
			// deadline is still audited.
			_ = CheckPermission(context.WithoutCancel(ctx), s.db, s.pluginID, de.missingPerm) // audit the denial
		}
	}
	return conn, err
}

// egressPolicy is what the caller holds for the host being dialled: exact
// (the exact permission for it, the only thing that unlocks a non-public
// address), lanOnly (it was admitted as plain http only through http:lan,
// so it must dial a non-public address) and needsHTTPLAN (the http rule,
// ADR-0121 §3: the plugin lacks http:lan, so exact unlocks loopback for a
// loopback host name only — never a LAN address; tcp_open leaves it false).
type egressPolicy struct {
	exact        bool
	lanOnly      bool
	needsHTTPLAN bool
}

// dialChecked resolves addr itself, checks every candidate IP, and connects
// only to an allowed one — by IP, so what is dialled is what was checked.
// pol.exact is whether the caller holds the exact permission for this host
// (net:<host> for http_request, tcp:<host>:<port> for tcp_open) — the only
// thing that unlocks a non-public address; pol.lanOnly refuses a public one.
// It also returns the IP it checked and connected to.
func (d *egressDialer) dialChecked(ctx context.Context, network, addr string, pol egressPolicy) (net.Conn, net.IP, error) {
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
		if err := d.check(host, ip, port, pol); err != nil {
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

func (d *egressDialer) check(host string, ip net.IP, port int, pol egressPolicy) error {
	deny := func(reason string) error {
		return &egressDeniedError{host: host, ip: ip.String(), reason: reason}
	}
	if ip.IsUnspecified() {
		return deny("unspecified address")
	}
	if isCloudMetadataIP(ip) {
		return deny("cloud metadata address")
	}
	if tp := d.tillPort(); tp != 0 && port == tp && d.isTillAddress(ip) {
		return deny("the till's own listen port")
	}
	public := isPublicIP(ip)
	if !public && !pol.exact {
		return deny("non-public address needs the exact permission for " + host)
	}
	if !public && pol.needsHTTPLAN && !(isLoopbackIP(ip) && isLoopbackHostName(host)) {
		return &egressDeniedError{host: host, ip: ip.String(),
			reason: "non-public address needs the " + permHTTPLAN + " permission", missingPerm: permHTTPLAN}
	}
	if public && pol.lanOnly {
		return deny("plain http under http:lan is LAN-only")
	}
	return nil
}

var (
	metadataIPv4 = netip.MustParseAddr("169.254.169.254")
	metadataIPv6 = netip.MustParseAddr("fd00:ec2::254")
)

// isCloudMetadataIP reports whether ip is a cloud instance-metadata address
// (169.254.169.254, or AWS's IPv6 fd00:ec2::254). Like isPublicIP it looks
// through the IPv4-mapped, NAT64 and 6to4 forms, so no spelling of the
// metadata address slips past the always-refused rule.
func isCloudMetadataIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if a.Is6() {
		b := a.As16()
		switch {
		case nat64Prefix.Contains(a):
			a = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
		case sixToFour.Contains(a):
			a = netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]})
		}
	}
	return a == metadataIPv4 || a == metadataIPv6
}

// isLoopbackIP reports whether ip is a loopback address, looking through
// the IPv4-mapped form (::ffff:127.0.0.1).
func isLoopbackIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	return ok && a.Unmap().IsLoopback()
}

// isLoopbackHostName reports whether the host a plugin asked for names the
// till itself: "localhost" or a loopback IP literal. Only such a host may
// reach loopback without http:lan (ADR-0121 §3, ut-docs#3794) — a LAN or
// public name that resolves to loopback may not.
func isLoopbackHostName(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && isLoopbackIP(ip)
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
	c := netaccess.NewClientWithTransport(2*time.Minute, tr) // timeout is a backstop; the event deadline is usually tighter
	c.CheckRedirect = checkPluginRedirect
	return c
}

// defaultPluginHTTPClient serves every plugin's http_request in production.
var defaultPluginHTTPClient = newPluginHTTPClient(defaultEgressDialer(), nil)

// checkPluginRedirect re-applies the scheme rule and the permission check to
// each redirect target (admitHTTPHop, the same rule as the first hop); the IP
// rule follows through the dialer, using the grant recorded here.
func checkPluginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxPluginRedirects {
		return fmt.Errorf("stopped after %d redirects", maxPluginRedirects)
	}
	host := req.URL.Hostname()
	ctx := req.Context()
	s, ok := stateFrom(ctx)
	if !ok {
		return &egressDeniedError{host: host, reason: "no plugin context"}
	}
	// Each hop's own grants decide — never the previous hop's: its
	// net:validation: grant (ut-docs#3226) or http:lan + exact grant
	// (ut-docs#3156) for plain http, its own name check.
	d, code, err := admitHTTPHop(ctx, s, req.URL)
	if code != 0 {
		if err != nil {
			return err
		}
		return &egressDeniedError{host: host, reason: "redirect to scheme " + req.URL.Scheme}
	}
	if g := egressGrantsFrom(ctx); g != nil {
		g.record(host, d)
	}
	return nil
}

// httpHopDecision is admitHTTPHop's verdict for one request hop: exact (the
// dialer may reach a non-public address), validation (a net:validation:
// host, ut-docs#3226), lanOnly (admitted only through http:lan — the dialer
// must reach a non-public address), httpLAN (the plugin holds http:lan, so
// exact also unlocks a LAN address, not just loopback — ut-docs#3794).
type httpHopDecision struct {
	exact, validation, lanOnly, httpLAN bool
}

// admitHTTPHop is the scheme rule and the name check for one hop of
// http_request or http_open, redirects included. code is 0 (admitted),
// hostErrInvalid (scheme refused — err nil) or hostErrDenied (permission
// refused — err says why, for the log).
//
//   - A net:validation:<host> host (ut-docs#3226) keeps the amendment's
//     rules unchanged: http or https, never exact, public-only — http:lan
//     plays no part.
//   - https, and plain http to loopback: the name check as before
//     (net:<host>, net:@setting, net:*). With http:lan, the host of one of
//     the plugin's own `type: "endpoint"` settings also passes, as an exact
//     grant — LAN-only, since only http:lan admitted it.
//   - plain http to any other host (ADR-0121 §2 http:lan): the plugin must
//     hold http:lan AND an exact grant for the host (net:<host>,
//     net:@setting) or the host must be an endpoint setting's; the hop is
//     LAN-only. Without http:lan it is a scheme refusal, exactly as before.
//
// http:lan is probed from the granted list (no audit row at the name check
// for a plugin that never asked for it); a plain-http request it would have
// admitted but which is refused for its absence is audited through
// CheckPermission, and the dialer audits a LAN dial refused for its absence,
// declared or not (ut-docs#3794).
func admitHTTPHop(ctx context.Context, s *hostState, u *url.URL) (httpHopDecision, int32, error) {
	host := u.Hostname()
	lookupFailed := func() (httpHopDecision, int32, error) {
		return httpHopDecision{}, hostErrDenied, &egressDeniedError{host: host, reason: "permission lookup failed"}
	}
	validation, err := validationGrantMatch(ctx, s.db, s.pluginID, host)
	if err != nil {
		return lookupFailed()
	}
	if validation {
		if !hostAllowedScheme(u, true) {
			return httpHopDecision{}, hostErrInvalid, nil
		}
		// A validation grant is never exact: public addresses only.
		return httpHopDecision{validation: true}, 0, nil
	}
	plainLAN := u.Scheme == "http" && !hostAllowedScheme(u, false)
	if !plainLAN && !hostAllowedScheme(u, false) {
		return httpHopDecision{}, hostErrInvalid, nil
	}
	exact, wildcard, err := netGrantMatch(ctx, s.db, s.pluginID, host)
	if err != nil {
		return lookupFailed()
	}
	// http:lan is probed for every hop, exact ones included: it decides
	// whether the exact grant reaches a LAN address (ut-docs#3794).
	endpoint := false
	perms, err := grantedPermissions(ctx, s.db, s.pluginID)
	if err != nil {
		return lookupFailed()
	}
	lan := containsString(perms, permHTTPLAN)
	if lan && !exact {
		endpoint, err = endpointSettingHostMatch(ctx, s.db, s.pluginID, host)
		if err != nil {
			// An unreadable manifest declares no endpoint (fail closed).
			logging.L().Warnf("[wasm:%s] endpoint settings unreadable: %v", s.pluginID, err)
			endpoint = false
		}
	}
	if plainLAN {
		if !lan {
			auditDeclaredDenial(ctx, s, permHTTPLAN)
			return httpHopDecision{}, hostErrInvalid, nil
		}
		if !exact && !endpoint {
			return httpHopDecision{}, hostErrInvalid, nil
		}
		return httpHopDecision{exact: true, lanOnly: true, httpLAN: true}, 0, nil
	}
	switch {
	case exact:
		return httpHopDecision{exact: true, httpLAN: lan}, 0, nil
	case endpoint:
		// The endpoint grant unlocks the LAN only; a net:* the plugin also
		// holds still covers public addresses, so the hop is LAN-only only
		// without it (review finding, ut-docs#3156).
		return httpHopDecision{exact: true, lanOnly: !wildcard, httpLAN: true}, 0, nil
	case wildcard:
		return httpHopDecision{}, 0, nil
	}
	_ = CheckPermission(ctx, s.db, s.pluginID, "net:"+host) // audit the denial
	return httpHopDecision{}, hostErrDenied, &egressDeniedError{host: host, reason: "no net:" + host + ", matching net:@setting or net:* permission"}
}

// auditDeclaredDenial audits a refusal for perm only when the plugin
// declared it (it is in the manifest but not granted): a plugin that never
// asked for perm gets a plain refusal, so a mistyped http:// URL in a poll
// loop cannot flood the audit log (review finding, ut-docs#3156).
func auditDeclaredDenial(ctx context.Context, s *hostState, perm string) {
	if _, declared, err := data.NewPluginRepo(s.db).CheckPermission(ctx, s.pluginID, perm); err == nil && declared {
		_ = CheckPermission(ctx, s.db, s.pluginID, perm)
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
