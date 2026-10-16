package languard

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
}

// serve runs one request from peer (host:port) through the guard.
func serve(t *testing.T, h http.Handler, peer, path string) int {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://till"+path, nil)
	r.RemoteAddr = peer
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func newGuard(t *testing.T, allow []string, own ...string) http.Handler {
	t.Helper()
	nets, err := ParseCIDRs(allow)
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}
	g := newWithOwn(okHandler(), nets, func() []net.IP {
		var ips []net.IP
		for _, s := range own {
			ips = append(ips, net.ParseIP(s))
		}
		return ips
	})
	return g
}

const lanPeer = "192.168.1.50:51234"

// The owner's report (ut-docs#4261): a phone on the shop Wi-Fi opened the
// till by its LAN address. Every staff surface must answer 403 to it —
// the PIN screen and exit-to-os included, so the PIN can't be brute-forced.
func TestLANCallerIsRefusedStaffSurfaces(t *testing.T) {
	g := newGuard(t, nil, "192.168.1.136")
	for _, p := range []string{
		"/", "/login", "/api/auth/login", "/api/auth/pin", "/api/settings/exit-to-os",
		"/setup", "/api/setup", "/api/setup/join", "/api/pos/basket", "/ui/sync-chip",
		"/settings", "/orders", "/kitchen-display/k1", "/api/window-mode", "/api/tables/t1/qr",
		"/api/syncfoo", "/api/sync", "/healthz/x",
		// a path that only looks like the sync surface until it is cleaned
		"/api/sync/../auth/login", "/o/../login",
		// not clean, so refused even though it cleans to an allowed path
		"/healthz/", "/api/sync/ping/", "/self-order/./cart", "/api/sync//ping",
	} {
		if got := serve(t, g, lanPeer, p); got != http.StatusForbidden {
			t.Errorf("LAN %s: got %d, want 403", p, got)
		}
	}
}

// Till-to-till sync (ADR-0011/0033/0114) and the guest pages that have not
// moved to the cloud yet (ADR-0157 §4) stay reachable from the LAN.
func TestLANCallerReachesSyncHealthAndGuestPages(t *testing.T) {
	g := newGuard(t, nil)
	for _, p := range []string{
		"/api/sync/ping", "/api/sync/pair-request", "/api/sync/link", "/api/sync/orders/r-1/status",
		"/healthz",
		"/self-order", "/self-order/cart", "/api/self-order/basket", "/o/abc123", "/o/abc123/status",
		"/public/app.css", "/themes/monarch/theme.css", "/plugin-icons/x.png",
		"/plugin-fonts/bundle-fonts.css", "/plugin-fonts/com.universaltill.language-th/2.0.0/NotoSansThai.woff2",
	} {
		if got := serve(t, g, lanPeer, p); got != http.StatusTeapot {
			t.Errorf("LAN %s: got %d, want passed through", p, got)
		}
	}
}

func TestLocalCallerReachesEverything(t *testing.T) {
	g := newGuard(t, nil, "192.168.1.136", "fe80::1")
	for _, peer := range []string{"127.0.0.1:5000", "[::1]:5000", "127.0.0.2:5000", "192.168.1.136:5000", "[fe80::1]:5000", "[fe80::1%eth0]:5000", "[::ffff:127.0.0.1]:5000", "[::ffff:192.168.1.136]:5000"} {
		for _, p := range []string{"/login", "/api/settings/exit-to-os", "/"} {
			if got := serve(t, g, peer, p); got != http.StatusTeapot {
				t.Errorf("%s %s: got %d, want passed through", peer, p, got)
			}
		}
	}
}

// UT_UI_ALLOW_CIDRS: a Docker host bridge or a dev laptop opts in explicitly.
func TestAllowedCIDRReachesEverything(t *testing.T) {
	g := newGuard(t, []string{"172.17.0.0/16", "10.0.0.7"})
	for _, peer := range []string{"172.17.0.1:4000", "10.0.0.7:4000"} {
		if got := serve(t, g, peer, "/login"); got != http.StatusTeapot {
			t.Errorf("%s /login: got %d, want passed through", peer, got)
		}
	}
	if got := serve(t, g, "10.0.0.8:4000", "/login"); got != http.StatusForbidden {
		t.Errorf("10.0.0.8 /login: got %d, want 403", got)
	}
}

// Only the TCP peer counts: a forwarded-for header claiming loopback is ignored.
func TestForwardedHeadersAreIgnored(t *testing.T) {
	g := newGuard(t, nil)
	r := httptest.NewRequest(http.MethodGet, "http://till/login", nil)
	r.RemoteAddr = lanPeer
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	r.Header.Set("X-Real-IP", "127.0.0.1")
	r.Header.Set("Forwarded", "for=127.0.0.1")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", w.Code)
	}
}

// An unparseable peer address is not local: fail closed.
func TestUnparseablePeerIsRefused(t *testing.T) {
	g := newGuard(t, nil)
	for _, peer := range []string{"", "garbage", "localhost:80", "[fe80::2%eth0]:5000", "[::ffff:192.168.1.50]:5000"} {
		if got := serve(t, g, peer, "/login"); got != http.StatusForbidden {
			t.Errorf("peer %q: got %d, want 403", peer, got)
		}
	}
}

func TestRefusalLeaksNothing(t *testing.T) {
	g := newGuard(t, nil)
	r := httptest.NewRequest(http.MethodGet, "http://till/login", nil)
	r.RemoteAddr = lanPeer
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("refusal redirected to %q", loc)
	}
	if body := w.Body.String(); body != "Forbidden\n" {
		t.Errorf("refusal body = %q, want the bare status text", body)
	}
}

func TestParseCIDRs(t *testing.T) {
	nets, err := ParseCIDRs([]string{" 172.17.0.0/16 ", "", "10.0.0.7", "fd00::/8"})
	if err != nil || len(nets) != 3 {
		t.Fatalf("got %v, %v; want 3 nets", nets, err)
	}
	if !nets[1].Contains(net.ParseIP("10.0.0.7")) || nets[1].Contains(net.ParseIP("10.0.0.8")) {
		t.Errorf("bare IP should be a single-host network, got %v", nets[1])
	}
	if _, err := ParseCIDRs([]string{"172.17.0.0/16", "not-a-cidr"}); err == nil {
		t.Errorf("want an error for an invalid entry")
	}
}

// ParseCIDRs must refuse an allow-everything entry: that is the guard turned off.
func TestParseCIDRsRefusesAllowAll(t *testing.T) {
	for _, s := range []string{"0.0.0.0/0", "::/0", "0.0.0.0/1"} {
		if _, err := ParseCIDRs([]string{s}); err == nil {
			t.Errorf("%s: want an error", s)
		}
	}
}

// The till opened through its own LAN address is local; a lookup error keeps
// the last good list rather than widening anything.
func TestOwnAddrCache(t *testing.T) {
	calls := 0
	fail := false
	c := &ownAddrCache{addr: func() ([]net.Addr, error) {
		calls++
		if fail {
			return nil, net.UnknownNetworkError("boom")
		}
		_, n, _ := net.ParseCIDR("192.168.1.136/24")
		n.IP = net.ParseIP("192.168.1.136")
		return []net.Addr{n}, nil
	}}
	g := newWithOwn(okHandler(), nil, c.get)
	if got := serve(t, g, "192.168.1.136:6000", "/login"); got != http.StatusTeapot {
		t.Fatalf("own address: got %d, want passed through", got)
	}
	if got := serve(t, g, "192.168.1.137:6000", "/login"); got != http.StatusForbidden {
		t.Fatalf("neighbour in the same /24: got %d, want 403", got)
	}
	if calls != 1 {
		t.Fatalf("lookup ran %d times within the TTL, want 1", calls)
	}
	fail = true
	c.at = c.at.Add(-2 * ownAddrsTTL)
	if got := serve(t, g, "192.168.1.136:6000", "/login"); got != http.StatusTeapot {
		t.Fatalf("after a failed refresh: got %d, want the last good list kept", got)
	}
}
