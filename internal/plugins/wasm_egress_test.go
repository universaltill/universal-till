package plugins

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// ut-docs#2891: http_request egress. The permission check used to look only
// at the URL's host NAME and send the request through http.DefaultClient —
// redirects unchecked, any resolved IP reachable. These tests drive the real
// wasip1 guest through the host function with a stubbed resolver/dialer so
// "public" names and LAN IPs can be simulated on a CI box.

func TestIsPublicIP(t *testing.T) {
	public := []string{"93.184.216.34", "8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "2a00:1450:4009:81f::200e",
		"64:ff9b::808:808", // NAT64 of 8.8.8.8
	}
	private := []string{
		"127.0.0.1", "127.9.9.9", "::1", "10.0.0.5", "172.16.0.1", "172.31.255.255", "192.168.1.50",
		"169.254.169.254", "fe80::1", "100.64.0.1", "100.127.255.254", "fc00::1", "fd12:3456::1",
		"224.0.0.1", "ff02::1", "0.0.0.0", "::", "255.255.255.255", "240.0.0.1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", // IPv4-mapped
		"64:ff9b::a00:1",                                         // NAT64 of 10.0.0.1
		"2002:a00:1::1",                                          // 6to4 of 10.0.0.1
		"2001:db8::1",                                            // documentation
		"192.0.2.1", "198.51.100.1", "203.0.113.1", "198.18.0.1", // test/benchmark nets
	}
	for _, s := range public {
		if !isPublicIP(net.ParseIP(s)) {
			t.Errorf("isPublicIP(%s) = false, want true", s)
		}
	}
	for _, s := range private {
		if isPublicIP(net.ParseIP(s)) {
			t.Errorf("isPublicIP(%s) = true, want false", s)
		}
	}
}

// egressTestCert is a self-signed cert valid for *.example.com and the LAN
// IPs the tests route, so https to "192.168.1.50" verifies like the real
// thing — no InsecureSkipVerify anywhere.
func egressTestCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "egress-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"example.com", "*.example.com"},
		IPAddresses:           []net.IP{net.ParseIP("192.168.1.50"), net.ParseIP("10.0.0.5"), net.ParseIP("192.168.1.20")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// egressEnv is one stubbed network: names resolve via dns, and a dial to
// any IP in route is delivered to that real test listener instead.
type egressEnv struct {
	dns      map[string][]string // host → IPs
	route    map[string]string   // IP → real listener addr
	tillPort int
}

func (e *egressEnv) client(pool *x509.CertPool) *http.Client {
	return newPluginHTTPClient(e.dialer(), &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
}

// dialer is the stubbed egressDialer itself — the http client above and
// tcp_open (withTCPEgress) share it, as they share it in production.
func (e *egressEnv) dialer() *egressDialer {
	return &egressDialer{
		lookup: func(_ context.Context, host string) ([]net.IP, error) {
			ips, ok := e.dns[host]
			if !ok {
				return nil, fmt.Errorf("no such host %s", host)
			}
			out := make([]net.IP, 0, len(ips))
			for _, s := range ips {
				out = append(out, net.ParseIP(s))
			}
			return out, nil
		},
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			if real, ok := e.route[host]; ok {
				addr = real
			}
			var nd net.Dialer
			return nd.DialContext(ctx, network, addr)
		},
		localIPs: func() []net.IP { return nil },
		tillPort: func() int { return e.tillPort },
	}
}

func hitCounter(n *atomic.Int32, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		_, _ = w.Write([]byte(body))
	})
}

func TestHTTPEgress(t *testing.T) {
	guest := buildHostfnGuest(t)
	cert, pool := egressTestCert(t)

	// One TLS server stands in for every https host; paths pick behaviour.
	var tlsHits, loopHits, tillHits atomic.Int32
	loop := httptest.NewServer(hitCounter(&loopHits, "loopback"))
	defer loop.Close()
	till := httptest.NewServer(hitCounter(&tillHits, "till"))
	defer till.Close()
	mux := http.NewServeMux()
	mux.Handle("/ok", hitCounter(&tlsHits, "ok"))
	mux.HandleFunc("/to-loopback", func(w http.ResponseWriter, r *http.Request) {
		tlsHits.Add(1)
		http.Redirect(w, r, loop.URL+"/x", http.StatusFound)
	})
	mux.HandleFunc("/to-lan", func(w http.ResponseWriter, r *http.Request) {
		tlsHits.Add(1)
		http.Redirect(w, r, "https://10.0.0.5/ok", http.StatusFound)
	})
	mux.HandleFunc("/to-other", func(w http.ResponseWriter, r *http.Request) {
		tlsHits.Add(1)
		http.Redirect(w, r, "https://other.example.com/ok", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		tlsHits.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	tlsSrv := httptest.NewUnstartedServer(mux)
	tlsSrv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	tlsSrv.StartTLS()
	defer tlsSrv.Close()
	tlsAddr := tlsSrv.Listener.Addr().String()
	tillPort, _ := strconv.Atoi(mustPort(t, till.URL))

	env := &egressEnv{
		dns: map[string][]string{
			"api.example.com":    {"93.184.216.34"},
			"other.example.com":  {"93.184.216.35"},
			"rebind.example.com": {"10.1.2.3"},
			"localhost":          {"127.0.0.1"},
		},
		route: map[string]string{
			"93.184.216.34": tlsAddr, "93.184.216.35": tlsAddr,
			"10.0.0.5": tlsAddr, "192.168.1.20": tlsAddr, "192.168.1.50": tlsAddr, "10.1.2.3": tlsAddr,
		},
		tillPort: tillPort,
	}

	type tc struct {
		name       string
		perms      []string
		url        string
		wantStatus int   // 0 → expect a failure code
		wantCode   int32 // when wantStatus == 0
		noHit      *atomic.Int32
	}
	cases := []tc{
		{name: "public host under net:*", perms: []string{"net:*"}, url: "https://api.example.com/ok", wantStatus: 200},
		{name: "exact public host", perms: []string{"net:api.example.com"}, url: "https://api.example.com/ok", wantStatus: 200},
		{name: "net:* refuses RFC1918 10.x literal", perms: []string{"net:*"}, url: "https://10.0.0.5/ok", wantCode: hostErrDenied, noHit: &tlsHits},
		{name: "net:* refuses RFC1918 192.168.x literal", perms: []string{"net:*"}, url: "https://192.168.1.20/ok", wantCode: hostErrDenied, noHit: &tlsHits},
		{name: "explicit net:192.168.1.50 allowed", perms: []string{"net:192.168.1.50"}, url: "https://192.168.1.50/ok", wantStatus: 200},
		{name: "DNS rebinding to a private IP under net:*", perms: []string{"net:*"}, url: "https://rebind.example.com/ok", wantCode: hostErrDenied, noHit: &tlsHits},
		{name: "net:* refuses plain http loopback", perms: []string{"net:*"}, url: loop.URL + "/x", wantCode: hostErrDenied, noHit: &loopHits},
		{name: "redirect from allowed https host to http://127.0.0.1 under net:*", perms: []string{"net:*"}, url: "https://api.example.com/to-loopback", wantCode: hostErrDenied, noHit: &loopHits},
		{name: "redirect to loopback without its permission", perms: []string{"net:api.example.com"}, url: "https://api.example.com/to-loopback", wantCode: hostErrDenied, noHit: &loopHits},
		{name: "redirect to a LAN IP under net:*", perms: []string{"net:*"}, url: "https://api.example.com/to-lan", wantCode: hostErrDenied},
		{name: "redirect to another public host under net:*", perms: []string{"net:*"}, url: "https://api.example.com/to-other", wantStatus: 200},
		{name: "redirect to an unpermitted host", perms: []string{"net:api.example.com"}, url: "https://api.example.com/to-other", wantCode: hostErrDenied},
		{name: "redirect loop capped", perms: []string{"net:*"}, url: "https://api.example.com/loop", wantCode: hostErrInternal},
		{name: "till's own port refused even with net:localhost", perms: []string{"net:localhost"}, url: "http://localhost:" + strconv.Itoa(tillPort) + "/x", wantCode: hostErrDenied, noHit: &tillHits},
		{name: "till's own port refused even with net:127.0.0.1", perms: []string{"net:127.0.0.1"}, url: "http://127.0.0.1:" + strconv.Itoa(tillPort) + "/x", wantCode: hostErrDenied, noHit: &tillHits},
		{name: "explicit net:localhost reaches another loopback port", perms: []string{"net:localhost"}, url: "http://localhost:" + mustPort(t, loop.URL) + "/x", wantStatus: 200},
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := hostfnTestDB(t)
			pluginID := fmt.Sprintf("com.test.egress%d", i)
			seedPlugin(t, d, pluginID)
			grantPerm(t, d, pluginID, "storage")
			for _, p := range c.perms {
				grantPerm(t, d, pluginID, p)
			}
			w := NewWasmRuntime(t.TempDir())
			w.httpClient = env.client(pool)
			if err := w.load(pluginID, "1.0.0", guest); err != nil {
				t.Fatalf("load: %v", err)
			}
			w.hasNet[pluginID] = true
			var before int32
			if c.noHit != nil {
				before = c.noHit.Load()
			}
			res := runGuest(t, w, d, pluginID, c.url)
			if c.wantStatus != 0 {
				if res["http_status"] != float64(c.wantStatus) {
					t.Fatalf("http_status = %v (code %v), want %d", res["http_status"], res["http_code"], c.wantStatus)
				}
				return
			}
			if res["http_code"] != float64(c.wantCode) {
				t.Fatalf("http_code = %v (status %v), want %d", res["http_code"], res["http_status"], c.wantCode)
			}
			if c.noHit != nil && c.noHit.Load() != before {
				t.Fatalf("refused request still reached the target server")
			}
		})
	}
	if n := tlsHits.Load(); n > 200 {
		t.Fatalf("redirect loop was not capped: %d hits", n)
	}
}

func mustPort(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

// SetTillListenAddr feeds the dial-time "never the till's own port" rule.
func TestSetTillListenAddr(t *testing.T) {
	prev := tillListenPort.Load()
	t.Cleanup(func() { tillListenPort.Store(prev) })
	cases := []struct {
		addr string
		want int32
	}{{":8080", 8080}, {"0.0.0.0:9090", 9090}, {"[::]:8081", 8081}, {"127.0.0.1:34029", 34029}}
	for _, c := range cases {
		SetTillListenAddr(c.addr)
		if got := tillListenPort.Load(); got != c.want {
			t.Errorf("SetTillListenAddr(%q) → %d, want %d", c.addr, got, c.want)
		}
	}
	SetTillListenAddr("garbage")
	if got := tillListenPort.Load(); got != 34029 {
		t.Errorf("unparseable addr changed the port to %d", got)
	}
}

// ut-docs#2891 security review minor: pin the redirect shapes and IP-literal
// spellings the first round didn't — a 307 (method AND body replayed), a
// relative and a scheme-relative Location, and loopback written as octal
// (0177.0.0.1), a single decimal (2130706433) or IPv4-mapped IPv6. The
// octal/decimal forms are not Go IP literals, so they reach the resolver; a
// lenient resolver (glibc/Darwin getaddrinfo accept inet_aton forms) is
// simulated by mapping them to 127.0.0.1. Every refusal must be the egress
// policy's (hostErrDenied), not a TLS or DNS failure, and must never reach
// the target handler.
func TestHTTPEgressRedirectShapesAndLiteralForms(t *testing.T) {
	guest := buildHostfnGuest(t)
	cert, pool := egressTestCert(t)

	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.Handle("/ok", hitCounter(&hits, "ok"))
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		_, _ = fmt.Fprintf(w, "%s %s", r.Method, b)
	})
	redirect := func(path, loc string, code int) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", loc)
			w.WriteHeader(code)
		})
	}
	redirect("/307-to-lan", "https://10.0.0.5/echo", http.StatusTemporaryRedirect)
	redirect("/307-to-other", "https://other.example.com/echo", http.StatusTemporaryRedirect)
	redirect("/308-to-loopback", "https://127.0.0.1/echo", http.StatusPermanentRedirect)
	redirect("/relative", "/ok", http.StatusFound)
	redirect("/relative-dot", "../ok", http.StatusFound)
	redirect("/scheme-relative-lan", "//10.0.0.5/ok", http.StatusFound)
	redirect("/to-octal", "https://0177.0.0.1/ok", http.StatusFound)
	redirect("/to-decimal", "https://2130706433/ok", http.StatusFound)
	redirect("/to-mapped", "https://[::ffff:127.0.0.1]/ok", http.StatusFound)
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	env := &egressEnv{
		dns: map[string][]string{
			"api.example.com":   {"93.184.216.34"},
			"other.example.com": {"93.184.216.35"},
			"0177.0.0.1":        {"127.0.0.1"},
			"2130706433":        {"127.0.0.1"},
			"0x7f.1":            {"127.0.0.1"},
		},
		// Every address — public, LAN or loopback — lands on the one test
		// server, so a wrongly-allowed dial would show up as a hit.
		route: map[string]string{
			"93.184.216.34": addr, "93.184.216.35": addr, "10.0.0.5": addr,
			"127.0.0.1": addr, "::ffff:127.0.0.1": addr,
		},
	}
	body := base64.StdEncoding.EncodeToString([]byte("card=4111"))

	cases := []struct {
		name       string
		perms      []string
		url        string
		post       bool
		wantStatus int
		wantBody   string
	}{
		{name: "307 with body to a LAN IP under net:*", perms: []string{"net:*"}, url: "https://api.example.com/307-to-lan", post: true},
		{name: "307 with body to an unpermitted host", perms: []string{"net:api.example.com"}, url: "https://api.example.com/307-to-other", post: true},
		{name: "307 with body to a permitted public host replays method and body", perms: []string{"net:*"}, url: "https://api.example.com/307-to-other", post: true, wantStatus: 200, wantBody: "POST card=4111"},
		{name: "308 with body to loopback under net:*", perms: []string{"net:*"}, url: "https://api.example.com/308-to-loopback", post: true},
		{name: "relative Location stays on the permitted host", perms: []string{"net:api.example.com"}, url: "https://api.example.com/relative", wantStatus: 200, wantBody: "ok"},
		{name: "dot-relative Location stays on the permitted host", perms: []string{"net:api.example.com"}, url: "https://api.example.com/relative-dot", wantStatus: 200, wantBody: "ok"},
		{name: "scheme-relative Location to a LAN IP under net:*", perms: []string{"net:*"}, url: "https://api.example.com/scheme-relative-lan"},
		{name: "redirect to octal loopback under net:*", perms: []string{"net:*"}, url: "https://api.example.com/to-octal"},
		{name: "redirect to decimal loopback under net:*", perms: []string{"net:*"}, url: "https://api.example.com/to-decimal"},
		{name: "redirect to IPv4-mapped loopback under net:*", perms: []string{"net:*"}, url: "https://api.example.com/to-mapped"},
		{name: "octal loopback literal under net:*", perms: []string{"net:*"}, url: "https://0177.0.0.1/ok"},
		{name: "decimal loopback literal under net:*", perms: []string{"net:*"}, url: "https://2130706433/ok"},
		{name: "hex-short loopback literal under net:*", perms: []string{"net:*"}, url: "https://0x7f.1/ok"},
		{name: "IPv4-mapped loopback literal under net:*", perms: []string{"net:*"}, url: "https://[::ffff:127.0.0.1]/ok"},
		{name: "IPv4-mapped loopback hex literal under net:*", perms: []string{"net:*"}, url: "https://[::ffff:7f00:1]/ok"},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := hostfnTestDB(t)
			pluginID := fmt.Sprintf("com.test.egressredir%d", i)
			seedPlugin(t, d, pluginID)
			grantPerm(t, d, pluginID, "storage")
			for _, p := range c.perms {
				grantPerm(t, d, pluginID, p)
			}
			w := NewWasmRuntime(t.TempDir())
			w.httpClient = env.client(pool)
			if err := w.load(pluginID, "1.0.0", guest); err != nil {
				t.Fatalf("load: %v", err)
			}
			w.hasNet[pluginID] = true
			payload := map[string]string{"url": c.url}
			if c.post {
				payload["method"] = http.MethodPost
				payload["body_b64"] = body
			}
			before := hits.Load()
			res := runGuestPayload(t, w, d, pluginID, payload)
			if c.wantStatus != 0 {
				if res["http_status"] != float64(c.wantStatus) {
					t.Fatalf("http_status = %v (code %v), want %d", res["http_status"], res["http_code"], c.wantStatus)
				}
				got, _ := base64.StdEncoding.DecodeString(fmt.Sprint(res["http_body"]))
				if string(got) != c.wantBody {
					t.Fatalf("body = %q, want %q", got, c.wantBody)
				}
				return
			}
			if res["http_code"] != float64(hostErrDenied) {
				t.Fatalf("http_code = %v (status %v), want %d (egress denied)", res["http_code"], res["http_status"], hostErrDenied)
			}
			if hits.Load() != before {
				t.Fatal("refused request still reached a target handler")
			}
		})
	}
}
